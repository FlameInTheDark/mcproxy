package upstream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type SSEClient struct {
	baseURL     string
	postURL     string
	client      *http.Client
	msgs        chan Message
	done        chan struct{}
	mu          sync.Mutex // Protects postURL and running state
	running     bool
	logger      *slog.Logger
	initialized chan struct{} // Closed when 'endpoint' event is received
}

func NewSSEClient(u string, logger *slog.Logger) *SSEClient {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &SSEClient{
		baseURL:     u,
		client:      &http.Client{Timeout: 0}, // No timeout for SSE stream
		msgs:        make(chan Message, 100),
		done:        make(chan struct{}),
		logger:      logger,
		initialized: make(chan struct{}),
	}
}

func (c *SSEClient) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("already running")
	}
	c.running = true
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	c.logger.Info("Connecting to SSE upstream", "url", c.baseURL)

	// Do the request in a goroutine so Start doesn't block
	go func() {
		resp, err := c.client.Do(req)
		if err != nil {
			c.logger.Error("Failed to connect to SSE upstream", "error", err)
			c.Close()
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			c.logger.Error("SSE upstream returned non-200 status", "status", resp.StatusCode)
			c.Close()
			return
		}

		c.readLoop(resp.Body)
	}()

	return nil
}

func (c *SSEClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	var eventType string
	var dataBuffer bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if dataBuffer.Len() > 0 {
				c.handleEvent(eventType, dataBuffer.Bytes())
				dataBuffer.Reset()
			}
			eventType = ""
			continue
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		field := parts[0]
		value := ""
		if len(parts) > 1 {
			value = strings.TrimPrefix(parts[1], " ")
		}

		switch field {
		case "event":
			eventType = value
		case "data":
			dataBuffer.WriteString(value)
			dataBuffer.WriteByte('\n')
		}
	}

	if err := scanner.Err(); err != nil {
		c.logger.Error("Error reading SSE stream", "error", err)
	}
	c.Close()
}

func (c *SSEClient) handleEvent(eventType string, data []byte) {
	// Trim trailing newlines from data accumulation
	data = bytes.TrimSpace(data)

	switch eventType {
	case "endpoint":
		endpoint := string(data)

		u, err := url.Parse(endpoint)
		if err != nil {
			c.logger.Error("Invalid endpoint URL received", "endpoint", endpoint, "error", err)
			return
		}

		base, err := url.Parse(c.baseURL)
		if err != nil {
			c.logger.Error("Invalid base URL", "url", c.baseURL, "error", err)
			return
		}

		resolved := base.ResolveReference(u).String()

		c.mu.Lock()
		c.postURL = resolved
		c.mu.Unlock()

		select {
		case <-c.initialized:
		default:
			close(c.initialized)
			c.logger.Info("SSE upstream initialized", "post_url", resolved)
		}

	case "message":
		msgCopy := make([]byte, len(data))
		copy(msgCopy, data)

		select {
		case c.msgs <- msgCopy:
		case <-c.done:
		}

	default:
		if eventType == "" && len(data) > 0 {
			msgCopy := make([]byte, len(data))
			copy(msgCopy, data)
			select {
			case c.msgs <- msgCopy:
			case <-c.done:
			}
		}
	}
}

func (c *SSEClient) Send(ctx context.Context, msg Message) error {
	select {
	case <-c.initialized:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout waiting for SSE initialization")
	}

	c.mu.Lock()
	targetURL := c.postURL
	c.mu.Unlock()

	if targetURL == "" {
		return fmt.Errorf("no post URL available")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upstream POST failed with status %d", resp.StatusCode)
	}

	return nil
}

func (c *SSEClient) Messages() <-chan Message {
	return c.msgs
}

func (c *SSEClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return nil
	}
	c.running = false
	close(c.done)
	return nil
}
