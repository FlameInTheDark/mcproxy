package transport

import (
	"bufio"
	"context"
	"fmt"
	"os"
)

func (s *Server) serveStdio(ctx context.Context) error {
	s.logger.Info("Starting Stdio Transport")

	scanner := bufio.NewScanner(os.Stdin)
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
			fmt.Fprintf(os.Stdout, "%s\n", resp)
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Error("Stdio scan error", "error", err)
		return err
	}

	return nil
}
