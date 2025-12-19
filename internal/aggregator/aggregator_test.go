package aggregator

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/FlameInTheDark/mcproxy/internal/mcp"
	"github.com/FlameInTheDark/mcproxy/internal/upstream"
)

// Reusing MockUpstream logic, but locally to avoid dependency cycle if I put it in a shared package not yet created.
type MockUpstream struct {
	sent     chan upstream.Message
	incoming chan upstream.Message
}

func NewMockUpstream() *MockUpstream {
	return &MockUpstream{
		sent:     make(chan upstream.Message, 100),
		incoming: make(chan upstream.Message, 100),
	}
}
func (m *MockUpstream) Start(ctx context.Context) error   { return nil }
func (m *MockUpstream) Close() error                      { return nil }
func (m *MockUpstream) Messages() <-chan upstream.Message { return m.incoming }
func (m *MockUpstream) Send(ctx context.Context, msg upstream.Message) error {
	m.sent <- msg
	return nil
}

func TestAggregator_Initialize(t *testing.T) {
	// Setup
	mock1 := NewMockUpstream()
	clients := map[string]*mcp.Client{
		"s1": mcp.NewClient(mock1, nil),
	}
	agg := NewAggregator(clients, slog.Default())

	// Test Initialize
	req := mcp.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05"}`),
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := agg.HandleMessage(context.Background(), reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var resp mcp.JSONRPCResponse
	json.Unmarshal(respBytes, &resp)

	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}

	var res mcp.InitializeResult
	json.Unmarshal(resp.Result, &res)
	if res.ServerInfo.Name != "mcproxy-aggregator" {
		t.Errorf("Expected mcproxy-aggregator, got %s", res.ServerInfo.Name)
	}
}

func TestAggregator_ListTools(t *testing.T) {
	mock1 := NewMockUpstream()
	clients := map[string]*mcp.Client{
		"s1": mcp.NewClient(mock1, slog.Default()),
	}
	agg := NewAggregator(clients, slog.New(slog.NewTextHandler(os.Stdout, nil)))

	// Handle upstream response
	go func() {
		// Expect tools/list request
		select {
		case msg := <-mock1.sent:
			var req mcp.JSONRPCRequest
			json.Unmarshal(msg, &req)
			if req.Method != "tools/list" {
				// t.Errorf here might be racy, just log
				return
			}

			// Respond with one tool
			toolsRes := mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "tool1", Description: "test tool"},
				},
			}
			resBytes, _ := json.Marshal(toolsRes)

			resp := mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  resBytes,
			}
			b, _ := json.Marshal(resp)
			mock1.incoming <- upstream.Message(b)
		case <-time.After(1 * time.Second):
			return
		}
	}()

	// Send tools/list to aggregator
	req := mcp.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}
	reqBytes, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	respBytes, err := agg.HandleMessage(ctx, reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var resp mcp.JSONRPCResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Error != nil {
		t.Fatalf("Response error: %v", resp.Error)
	}

	var listRes mcp.ListToolsResult
	json.Unmarshal(resp.Result, &listRes)

	if len(listRes.Tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(listRes.Tools))
	}
	if listRes.Tools[0].Name != "tool1" {
		t.Errorf("Expected tool1, got %s", listRes.Tools[0].Name)
	}
}

func TestAggregator_CallTool(t *testing.T) {
	mock1 := NewMockUpstream()
	clients := map[string]*mcp.Client{
		"s1": mcp.NewClient(mock1, slog.Default()),
	}
	agg := NewAggregator(clients, slog.Default())

	// Pre-populate routing map (hack for test since we didn't run ListTools in this test)
	// Or better, simulate ListTools first.
	// But `toolMap` is private.
	// So we MUST run ListTools flow first to populate the map.

	go func() {
		// 1. Handle ListTools
		msg := <-mock1.sent
		var req mcp.JSONRPCRequest
		json.Unmarshal(msg, &req)

		toolsRes := mcp.ListToolsResult{
			Tools: []mcp.Tool{{Name: "tool1"}},
		}
		resBytes, _ := json.Marshal(toolsRes)
		resp := mcp.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: resBytes}
		b, _ := json.Marshal(resp)
		mock1.incoming <- upstream.Message(b)

		// 2. Handle CallTool
		msg = <-mock1.sent
		json.Unmarshal(msg, &req)
		if req.Method != "tools/call" {
			return
		}

		callRes := json.RawMessage(`"called"`)
		resp = mcp.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: callRes}
		b, _ = json.Marshal(resp)
		mock1.incoming <- upstream.Message(b)
	}()

	ctx := context.Background()

	// 1. List Tools to populate map
	listReq := mcp.JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "tools/list"}
	b, _ := json.Marshal(listReq)
	agg.HandleMessage(ctx, b)

	// 2. Call Tool
	callParams := mcp.CallToolParams{Name: "tool1"}
	paramBytes, _ := json.Marshal(callParams)
	callReq := mcp.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params:  paramBytes,
	}
	b, _ = json.Marshal(callReq)

	respBytes, err := agg.HandleMessage(ctx, b)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	var resp mcp.JSONRPCResponse
	json.Unmarshal(respBytes, &resp)

	if resp.Error != nil {
		t.Errorf("Call returned error: %v", resp.Error)
	}
	var resStr string
	json.Unmarshal(resp.Result, &resStr)
	if resStr != "called" {
		t.Errorf("Expected 'called', got %s", resStr)
	}
}
