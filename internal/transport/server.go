package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"sync"

	"github.com/FlameInTheDark/mcproxy/internal/aggregator"
	"github.com/FlameInTheDark/mcproxy/internal/config"
	"github.com/FlameInTheDark/mcproxy/internal/mcp"
	"github.com/FlameInTheDark/mcproxy/internal/upstream"
)

type ClientFactory func(cfg config.ServerConfig, logger *slog.Logger) (upstream.Client, error)

func DefaultClientFactory(cfg config.ServerConfig, logger *slog.Logger) (upstream.Client, error) {
	switch cfg.Type {
	case "stdio":
		return upstream.NewStdioClient(cfg.Command, cfg.Args, cfg.Env, logger.With("upstream", cfg.Name)), nil
	case "sse", "http":
		return upstream.NewSSEClient(cfg.URL, logger.With("upstream", cfg.Name)), nil
	default:
		return nil, fmt.Errorf("unknown server type: %s", cfg.Type)
	}
}

type Server struct {
	upstreams  map[string]upstream.Client
	clients    map[string]*mcp.Client
	aggregator *aggregator.Aggregator
	logger     *slog.Logger
	port       int
	transport  string

	// Session Management
	sessionsMu sync.RWMutex
	sessions   map[string]chan []byte
}

func NewServer(cfg *config.Config, logger *slog.Logger, factory ClientFactory) (*Server, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if factory == nil {
		factory = DefaultClientFactory
	}

	s := &Server{
		upstreams: make(map[string]upstream.Client),
		clients:   make(map[string]*mcp.Client),
		sessions:  make(map[string]chan []byte),
		logger:    logger,
		port:      cfg.Server.Port,
		transport: cfg.Server.Transport,
	}

	for _, srvCfg := range cfg.MCPServers {
		client, err := factory(srvCfg, logger)
		if err != nil {
			return nil, err
		}
		s.upstreams[srvCfg.Name] = client
		// Wrap in MCP Client
		s.clients[srvCfg.Name] = mcp.NewClient(client, logger.With("component", "mcp_client", "upstream", srvCfg.Name))
	}

	s.aggregator = aggregator.NewAggregator(s.clients, logger)

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	// Start all upstreams
	for name, client := range s.upstreams {
		if err := client.Start(ctx); err != nil {
			return fmt.Errorf("failed to start upstream %s: %w", name, err)
		}
		defer client.Close()
	}

	// Initialize upstreams via aggregator
	go s.aggregator.Start(ctx)

	if s.transport == "stdio" {
		return s.serveStdio(ctx)
	}

	mux := http.NewServeMux()
	// Direct access
	mux.HandleFunc("GET /mcp/{server}/sse", s.handleSSE)
	mux.HandleFunc("POST /mcp/{server}/messages", s.handleMessage)

	// Aggregated access
	mux.HandleFunc("GET /sse", s.handleAggregatedSSE)
	mux.HandleFunc("POST /messages", s.handleAggregatedMessage)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: s.loggingMiddleware(mux),
	}

	s.logger.Info("Server starting", "port", s.port)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Only log non-SSE/Message requests here, or log all?
		// Logging every SSE chunk might be too much, but connection open is good.
		s.logger.Info("HTTP Request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
		s.logger.Info("HTTP Request completed", "path", r.URL.Path, "duration", time.Since(start))
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	serverName := r.PathValue("server")
	client, ok := s.clients[serverName]
	if !ok {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Send endpoint event
	// The endpoint should be the full URL where the client can POST messages
	// Scheme/Host might be missing from request if behind proxy, but we try our best.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	endpoint := fmt.Sprintf("%s://%s/mcp/%s/messages", scheme, host, serverName)

	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	s.logger.Info("Client connected to SSE", "server", serverName)

	msgChan := client.Passthrough() // Use Passthrough from Wrapper

	// Create a context that cancels when the client disconnects
	ctx := r.Context()

	for {
		select {
		case msg, ok := <-msgChan:
			if !ok {
				s.logger.Info("Upstream closed", "server", serverName)
				return
			}
			// Forward message
			// Simple SSE format: event: message\ndata: <json>\n\n
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ctx.Done():
			s.logger.Info("Client disconnected from SSE", "server", serverName)
			return
		}
	}
}
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	serverName := r.PathValue("server")
	client, ok := s.upstreams[serverName]
	if !ok {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Log the MCP Request details
	var rawMsg map[string]interface{}
	if err := json.Unmarshal(body, &rawMsg); err == nil {
		// Try to extract method/id for structured logging
		method, _ := rawMsg["method"].(string)
		id, _ := rawMsg["id"]

		// If it's a notification, id is nil (or missing)
		if method != "" {
			s.logger.Info("MCP Request",
				"server", serverName,
				"method", method,
				"id", id,
				"params_size", len(body),
			)
		} else {
			// Response or other
			s.logger.Info("MCP Message", "server", serverName, "type", "response/other", "size", len(body))
		}
	} else {
		s.logger.Warn("Failed to parse MCP message JSON", "server", serverName, "error", err)
	}

	if err := client.Send(r.Context(), upstream.Message(body)); err != nil {
		s.logger.Error("Failed to send message to upstream", "server", serverName, "error", err)
		http.Error(w, "Failed to forward message", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
