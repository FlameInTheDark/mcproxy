package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/FlameInTheDark/mcproxy/internal/upstream"
)

type MockUpstream struct {
	sent     chan upstream.Message
	incoming chan upstream.Message
}

func NewMockUpstream() *MockUpstream {
	return &MockUpstream{
		sent:     make(chan upstream.Message, 10),
		incoming: make(chan upstream.Message, 10),
	}
}

func (m *MockUpstream) Start(ctx context.Context) error { return nil }
func (m *MockUpstream) Close() error                    { return nil }
func (m *MockUpstream) Messages() <-chan upstream.Message {
	return m.incoming
}
func (m *MockUpstream) Send(ctx context.Context, msg upstream.Message) error {
	m.sent <- msg
	return nil
}

func TestClient_Call(t *testing.T) {
	mock := NewMockUpstream()
	client := NewClient(mock, nil)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Send Request
	go func() {
		// Verify request sent to upstream
		select {
		case msg := <-mock.sent:
			var req JSONRPCRequest
			if err := json.Unmarshal(msg, &req); err != nil {
				t.Errorf("Failed to unmarshal request: %v", err)
				return
			}
			if req.Method != "test_method" {
				t.Errorf("Expected method test_method, got %s", req.Method)
			}

			// Send Response
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`"success"`),
			}
			b, _ := json.Marshal(resp)
			mock.incoming <- upstream.Message(b)
		case <-ctx.Done():
			t.Errorf("Timeout waiting for upstream send")
		}
	}()

	resp, err := client.Call(ctx, "test_method", nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	var resStr string
	if err := json.Unmarshal(resp.Result, &resStr); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}
	if resStr != "success" {
		t.Errorf("Expected 'success', got %s", resStr)
	}
}

func TestClient_Passthrough(t *testing.T) {
	mock := NewMockUpstream()
	client := NewClient(mock, nil)
	defer client.Close()

	// 1. Send Notification (no ID) from upstream
	notif := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notify",
	}
	b, _ := json.Marshal(notif)
	mock.incoming <- upstream.Message(b)

	// 2. Receive in Passthrough
	select {
	case msg := <-client.Passthrough():
		if string(msg) != string(b) {
			t.Errorf("Expected %s, got %s", b, msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for passthrough")
	}
}
