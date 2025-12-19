package transport

import (
	"crypto/rand"
	"encoding/hex"
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

	// Create Session
	sessionID := generateSessionID()
	msgChan := make(chan []byte, 100)

	s.sessionsMu.Lock()
	s.sessions[sessionID] = msgChan
	s.sessionsMu.Unlock()

	defer func() {
		s.sessionsMu.Lock()
		delete(s.sessions, sessionID)
		s.sessionsMu.Unlock()
		s.logger.Info("Aggregated SSE session ended", "sessionID", sessionID)
	}()

	scheme := "http"

	if r.TLS != nil {

		scheme = "https"

	}

	endpoint := fmt.Sprintf("%s://%s/messages?sessionID=%s", scheme, r.Host, sessionID)

	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)

	flusher.Flush()

	s.logger.Info("Client connected to Aggregated SSE", "sessionID", sessionID)

	ctx := r.Context()
	for {
		select {
		case msg := <-msgChan:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handleAggregatedMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionID")
	if sessionID == "" {
		http.Error(w, "Missing sessionID", http.StatusBadRequest)
		return
	}

	s.sessionsMu.RLock()
	msgChan, ok := s.sessions[sessionID]
	s.sessionsMu.RUnlock()

	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

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
		// Route response to the specific session
		select {
		case msgChan <- resp:
		default:
			s.logger.Warn("Session buffer full, dropping message", "sessionID", sessionID)
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

func generateSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
