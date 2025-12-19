package transport

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FlameInTheDark/mcproxy/internal/config"
	"github.com/FlameInTheDark/mcproxy/internal/upstream"
)

type MockClient struct {
	msgs chan upstream.Message
	sent []upstream.Message
}

func (m *MockClient) Start(ctx context.Context) error { return nil }
func (m *MockClient) Close() error                    { return nil }
func (m *MockClient) Messages() <-chan upstream.Message {
	return m.msgs
}
func (m *MockClient) Send(ctx context.Context, msg upstream.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}

func TestServerFlow(t *testing.T) {
	// Setup
	mock := &MockClient{
		msgs: make(chan upstream.Message, 10),
	}

	factory := func(cfg config.ServerConfig, logger *slog.Logger) (upstream.Client, error) {
		return mock, nil
	}

	cfg := &config.Config{}
	cfg.MCPServers = append(cfg.MCPServers, config.ServerConfig{Name: "test", Type: "mock"})
	cfg.Server.Port = 0

	srv, err := NewServer(cfg, slog.Default(), factory)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp/{server}/sse", srv.handleSSE)
	mux.HandleFunc("POST /mcp/{server}/messages", srv.handleMessage)

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Connect SSE
	sseURL := server.URL + "/mcp/test/sse"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	// Don't close Body immediately, we want to read stream

	reader := bufio.NewReader(resp.Body)

	// Read "endpoint" event
	line, err := reader.ReadString('\n') // event: endpoint\n
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "endpoint") {
		t.Fatalf("Expected endpoint event, got: %s", line)
	}

	line, err = reader.ReadString('\n') // data: ...\n
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "data:") {
		t.Fatalf("Expected data line, got: %s", line)
	}
	endpointURL := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

	reader.ReadString('\n') // Empty line

	// 2. Send Message (POST)
	if !strings.Contains(endpointURL, "/mcp/test/messages") {
		t.Errorf("Unexpected endpoint: %s", endpointURL)
	}

	// Send a POST
	msgBody := `{"jsonrpc":"2.0","method":"ping","id":1}`
	postReq, _ := http.NewRequest("POST", endpointURL, bytes.NewBufferString(msgBody))
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		t.Errorf("Expected 202, got %d", postResp.StatusCode)
	}

	// Check mock received it
	if len(mock.sent) != 1 {
		t.Fatalf("Expected 1 message sent to mock, got %d", len(mock.sent))
	}
	if string(mock.sent[0]) != msgBody {
		t.Errorf("Message mismatch")
	}

	// 3. Receive Message (Upstream -> Downstream)
	upstreamMsg := []byte(`{"jsonrpc":"2.0","result":"pong","id":1}`)
	mock.msgs <- upstreamMsg

	// Read from SSE stream
	// Should get event: message ...

	line, err = reader.ReadString('\n') // event: message
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "message") {
		t.Errorf("Expected message event, got: %s", line)
	}

	line, err = reader.ReadString('\n') // data: ...
	if err != nil {
		t.Fatal(err)
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

	if data != string(upstreamMsg) {
		t.Errorf("Expected %s, got %s", upstreamMsg, data)
	}
}
