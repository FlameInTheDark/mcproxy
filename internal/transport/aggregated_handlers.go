package transport

import (
	"fmt"
	"io"
	"net/http"
)

func (s *Server) handleAggregatedSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://%s/messages", scheme, r.Host)

	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	s.logger.Info("Client connected to Aggregated SSE")

	ctx := r.Context()
	for {
		select {
		case msg := <-s.aggregatorUpdates:
			// Forward message
			// Simple SSE format: event: message\ndata: <json>\n\n
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ctx.Done():
			s.logger.Info("Client disconnected from Aggregated SSE")
			return
		}
	}
}

func (s *Server) handleAggregatedMessage(w http.ResponseWriter, r *http.Request) {

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	resp, err := s.aggregator.HandleMessage(r.Context(), body)
	if err != nil {
		s.logger.Error("Aggregator error", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp != nil {
		s.logger.Warn("Aggregated mode: Sending response to ALL connected clients (Single User assumption)")
		s.aggregatorUpdates <- resp
	}

	w.WriteHeader(http.StatusAccepted)
}
