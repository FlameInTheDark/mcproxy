package upstream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

type StdioClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	msgs    chan Message
	done    chan struct{}
	mu      sync.Mutex
	running bool
	logger  *slog.Logger
}

func NewStdioClient(command string, args []string, env []string, logger *slog.Logger) *StdioClient {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(), env...)

	return &StdioClient{
		cmd:    cmd,
		msgs:   make(chan Message, 100),
		done:   make(chan struct{}),
		logger: logger,
	}
}

func (c *StdioClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("already running")
	}

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	c.stderr, err = c.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	c.running = true
	c.logger.Info("Stdio upstream started", "cmd", c.cmd.Path, "args", c.cmd.Args)

	// stdout reader
	go c.readLoop(c.stdout, "stdout")
	// stderr reader
	go c.readStderr(c.stderr)

	// monitor process exit
	go func() {
		err := c.cmd.Wait()
		if err != nil {
			c.logger.Error("Command exited with error", "error", err)
		} else {
			c.logger.Info("Command exited successfully")
		}
		c.Close()
	}()

	return nil
}

func (c *StdioClient) readLoop(r io.Reader, name string) {
	scanner := bufio.NewScanner(r)
	// Increase buffer size if necessary, but default 64k is usually okay for chunks.
	// MCP messages can be large, so we might need a larger buffer.
	const maxCapacity = 10 * 1024 * 1024 // 10MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		msg := scanner.Bytes()
		// Make a copy because scanner reuses the buffer
		msgCopy := make([]byte, len(msg))
		copy(msgCopy, msg)

		select {
		case c.msgs <- msgCopy:
		case <-c.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.logger.Error("Error reading from "+name, "error", err)
	}
}

func (c *StdioClient) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		c.logger.Warn("Upstream stderr", "content", scanner.Text())
	}
}

func (c *StdioClient) Send(ctx context.Context, msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return fmt.Errorf("client not running")
	}

	// Append newline as required by MCP stdio transport
	_, err := c.stdin.Write(append(msg, '\n'))
	return err
}

func (c *StdioClient) Messages() <-chan Message {
	return c.msgs
}

func (c *StdioClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return nil
	}
	c.running = false
	close(c.done)

	// Try to terminate gracefully
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	return nil
}
