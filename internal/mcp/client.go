package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/FlameInTheDark/mcproxy/internal/upstream"
)

// Client wraps an upstream.Client to provide structured JSON-RPC interaction.
type Client struct {
	upstream upstream.Client
	logger   *slog.Logger

	pendingMu sync.Mutex
	pending   map[string]chan JSONRPCResponse

	passthrough chan []byte
	done        chan struct{}

	idCounter atomic.Int64
}

func NewClient(u upstream.Client, logger *slog.Logger) *Client {
	c := &Client{
		upstream:    u,
		logger:      logger,
		pending:     make(map[string]chan JSONRPCResponse),
		passthrough: make(chan []byte, 100),
		done:        make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer close(c.passthrough)
	for {
		select {
		case msgBytes, ok := <-c.upstream.Messages():
			if !ok {
				c.closePending()
				return
			}

			var msg JSONRPCMessage
			if err := json.Unmarshal(msgBytes, &msg); err != nil {
				c.logger.Error("Failed to unmarshal upstream message", "error", err)
				// Forward corrupted/unknown messages too? Yes, let downstream handle or see it.
				select {
				case c.passthrough <- msgBytes:
				case <-c.done:
					return
				}
				continue
			}

			// If it has an ID, check if it's a response to a pending request
			if msg.ID != nil {
				idStr := fmt.Sprintf("%v", msg.ID)
				c.pendingMu.Lock()
				ch, ok := c.pending[idStr]
				if ok {
					delete(c.pending, idStr)
					c.pendingMu.Unlock()

					// Reconstruct Response from Message
					resp := JSONRPCResponse{
						JSONRPC: msg.JSONRPC,
						Result:  msg.Result,
						Error:   msg.Error,
						ID:      msg.ID,
					}
					select {
					case ch <- resp:
					default:
					}
					continue
				}
				c.pendingMu.Unlock()
			}

			// Otherwise treat as notification or request from server to client
			select {
			case c.passthrough <- msgBytes:
			case <-c.done:
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) closePending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = nil
}

func (c *Client) Call(ctx context.Context, method string, params interface{}) (*JSONRPCResponse, error) {
	id := c.idCounter.Add(1)
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		ID:      id,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		req.Params = b
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	respCh := make(chan JSONRPCResponse, 1)
	idStr := fmt.Sprintf("%v", id)

	c.pendingMu.Lock()
	c.pending[idStr] = respCh
	c.pendingMu.Unlock()

	// Ensure cleanup if context cancelled or send failed
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, idStr)
		c.pendingMu.Unlock()
	}()

	if err := c.upstream.Send(ctx, upstream.Message(reqBytes)); err != nil {
		return nil, err
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("connection closed")
		}
		return &resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) Notify(ctx context.Context, method string, params interface{}) error {
	req := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
	}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}

	return c.upstream.Send(ctx, upstream.Message(reqBytes))
}

func (c *Client) Passthrough() <-chan []byte {
	return c.passthrough
}

func (c *Client) Close() {
	close(c.done)
}
