package transport

import (
	"bufio"
	"context"
	"fmt"
	"os"
)

func (s *Server) serveStdio(ctx context.Context) error {
	s.logger.Info("Starting Stdio Transport")

	// Goroutine to read from aggregator and write to Stdout
	go func() {
		for {
			select {
			case msg := <-s.aggregatorUpdates:
				// Write message to stdout with newline
				// Use fmt.Printf or similar, but be careful with concurrency if logger also writes to stdout (it shouldn't)
				fmt.Fprintf(os.Stdout, "%s\n", msg)
			case <-ctx.Done():
				return
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer size
	const maxCapacity = 10 * 1024 * 1024
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		msgBytes := scanner.Bytes()
		// Copy because scanner reuses
		msgCopy := make([]byte, len(msgBytes))
		copy(msgCopy, msgBytes)

		// Process via Aggregator
		resp, err := s.aggregator.HandleMessage(ctx, msgCopy)
		if err != nil {
			s.logger.Error("Error handling stdio message", "error", err)
			continue
		}

		if resp != nil {
			// Write immediate response
			fmt.Fprintf(os.Stdout, "%s\n", resp)
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Error("Stdio scan error", "error", err)
		return err
	}

	return nil
}
