package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/FlameInTheDark/mcproxy/internal/mcp"
)

type Aggregator struct {
	clients map[string]*mcp.Client
	logger  *slog.Logger

	// Tool Routing Table
	mu      sync.RWMutex
	toolMap map[string]string // toolName -> serverName
}

func NewAggregator(clients map[string]*mcp.Client, logger *slog.Logger) *Aggregator {
	return &Aggregator{
		clients: clients,
		logger:  logger,
		toolMap: make(map[string]string),
	}
}

func (a *Aggregator) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for name, client := range a.clients {
		wg.Add(1)
		go func(n string, c *mcp.Client) {
			defer wg.Done()

			// Initialize params: The Proxy acts as the Client "mcproxy"
			initParams := mcp.InitializeParams{
				ProtocolVersion: "2024-11-05",
				Capabilities:    json.RawMessage(`{}`), // Proxy itself has no client capabilities (e.g. sampling) implemented yet
				ClientInfo:      json.RawMessage(`{"name":"mcproxy","version":"1.0.0"}`),
			}

			resp, err := c.Call(ctx, "initialize", initParams)
			if err != nil {
				a.logger.Error("Failed to initialize upstream", "upstream", n, "error", err)
				return
			}

			if resp.Error != nil {
				a.logger.Error("Upstream rejected initialization", "upstream", n, "error", resp.Error.Message)
				return
			}

			// Send initialized notification
			if err := c.Notify(ctx, "notifications/initialized", nil); err != nil {
				a.logger.Error("Failed to send initialized notification", "upstream", n, "error", err)
			}

			a.logger.Info("Upstream initialized", "upstream", n)
		}(name, client)
	}
	wg.Wait()
}

// HandleMessage processes a message from the downstream client.
// Returns the response bytes to send back, or nil if none.
func (a *Aggregator) HandleMessage(ctx context.Context, msgBytes []byte) ([]byte, error) {
	var msg mcp.JSONRPCMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		return nil, err
	}

	if msg.Method != "" && msg.ID != nil {
		switch msg.Method {
		case "initialize":
			return a.handleInitialize(ctx, msg)
		case "tools/list":
			return a.handleListTools(ctx, msg)
		case "tools/call":
			return a.handleCallTool(ctx, msg)
		default:
			return a.jsonRPCError(msg.ID, -32601, "Method not supported in aggregated mode"), nil
		}
	}

	return nil, nil
}

func (a *Aggregator) handleInitialize(ctx context.Context, msg mcp.JSONRPCMessage) ([]byte, error) {
	var params mcp.InitializeParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return a.jsonRPCError(msg.ID, -32700, "Invalid params"), nil
	}

	// We simply return a success response claiming we are the proxy.
	// We do NOT handshake with upstreams here yet, because they might already be running
	// or we might want to lazy load.
	// Actually, `upstream.Client.Start()` is called by `Server.Start()`, so they are running.
	// Ideally, we should initialize them now using the client's capabilities?
	// But upstreams are shared! If another client connects, we can't re-initialize.
	// Assumption: Upstreams are initialized once or use default.
	// But MCP requires per-connection initialization.
	// Since `upstream.Client` (Stdio) is a single process, it expects one init.
	// Since `upstream.Client` (SSE) is HTTP, it might be stateless or session-based.
	//
	// CRITICAL: Stdio processes are 1-to-1. If we have multiple clients connecting to the Proxy,
	// and the Proxy shares one Stdio process, the state is shared.
	// This "Shared Proxy" model implies the Proxy is the "Client" to the Upstream,
	// and the Proxy is the "Server" to the Downstream users.
	// The Proxy should initialize the Upstream ONCE when it starts.

	// FIXME: We haven't initialized the upstreams in `Server.Start`.
	// For this prototype, let's assume upstreams are lenient or we accept that
	// we initialize them on the first request?
	// Better: The Aggregator shouldn't forward `initialize` blindly.
	// It should just return its OWN capabilities.

	result := mcp.InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities:    json.RawMessage(`{"tools":{}}`), // We support tools
		ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{
			Name:    "mcproxy-aggregator",
			Version: "1.0.0",
		},
	}

	resBytes, _ := json.Marshal(result)
	return a.jsonRPCResponse(msg.ID, resBytes), nil
}

func (a *Aggregator) handleListTools(ctx context.Context, msg mcp.JSONRPCMessage) ([]byte, error) {
	var allTools []mcp.Tool

	// Query all upstreams
	// This assumes upstreams are already initialized!
	// If they are not, `Call` might fail or they might error.
	// We'll proceed assuming they respond.

	// We need to sync this to populate `toolMap`
	a.mu.Lock()
	defer a.mu.Unlock()

	// Clear old map
	a.toolMap = make(map[string]string)

	var wg sync.WaitGroup
	var lock sync.Mutex

	for name, client := range a.clients {
		wg.Add(1)
		go func(n string, c *mcp.Client) {
			defer wg.Done()
			// Send tools/list
			resp, err := c.Call(ctx, "tools/list", nil)
			if err != nil {
				a.logger.Error("Failed to list tools", "upstream", n, "error", err)
				return
			}
			if resp.Error != nil {
				a.logger.Error("Upstream error listing tools", "upstream", n, "error", resp.Error.Message)
				return
			}

			var listRes mcp.ListToolsResult
			if err := json.Unmarshal(resp.Result, &listRes); err != nil {
				a.logger.Error("Failed to unmarshal tools list", "upstream", n, "error", err)
				return
			}

			lock.Lock()
			for _, t := range listRes.Tools {
				// Naive merge: last one wins on name collision
				allTools = append(allTools, t)
				a.toolMap[t.Name] = n
			}
			lock.Unlock()
		}(name, client)
	}
	wg.Wait()

	res := mcp.ListToolsResult{Tools: allTools}
	resBytes, _ := json.Marshal(res)
	return a.jsonRPCResponse(msg.ID, resBytes), nil
}

func (a *Aggregator) handleCallTool(ctx context.Context, msg mcp.JSONRPCMessage) ([]byte, error) {
	var params mcp.CallToolParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return a.jsonRPCError(msg.ID, -32700, "Invalid params"), nil
	}

	a.mu.RLock()
	upstreamName, ok := a.toolMap[params.Name]
	a.mu.RUnlock()

	if !ok {
		return a.jsonRPCError(msg.ID, -32601, fmt.Sprintf("Tool not found: %s", params.Name)), nil
	}

	client, ok := a.clients[upstreamName]
	if !ok {
		return a.jsonRPCError(msg.ID, -32603, "Upstream client missing"), nil
	}

	// Forward the call
	resp, err := client.Call(ctx, "tools/call", params)
	if err != nil {
		return a.jsonRPCError(msg.ID, -32603, fmt.Sprintf("Upstream call failed: %v", err)), nil
	}

	if resp.Error != nil {
		// Forward error
		errResp := mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   resp.Error,
		}
		b, _ := json.Marshal(errResp)
		return b, nil
	}

	return a.jsonRPCResponse(msg.ID, resp.Result), nil
}

func (a *Aggregator) jsonRPCError(id interface{}, code int, message string) []byte {
	resp := mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcp.JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func (a *Aggregator) jsonRPCResponse(id interface{}, result json.RawMessage) []byte {
	resp := mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	b, _ := json.Marshal(resp)
	return b
}
