package upstream

import (
	"context"
)

// Message represents a raw JSON message (request or response)
type Message []byte

// Client is the interface for an MCP upstream connection.
type Client interface {
	// Start initializes the connection/process.
	Start(ctx context.Context) error
	// Send sends a message to the upstream server.
	Send(ctx context.Context, msg Message) error
	// Messages returns a channel to receive messages from the upstream server.
	Messages() <-chan Message
	// Close terminates the connection.
	Close() error
}
