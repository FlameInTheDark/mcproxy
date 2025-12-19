package config

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	content := `
mcpServers:
  - name: "test-server"
    type: "stdio"
    command: "echo"
    args: ["hello"]
server:
  port: 9090
`
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.MCPServers) != 1 {
		t.Errorf("expected 1 server, got %d", len(cfg.MCPServers))
	}
	if cfg.MCPServers[0].Name != "test-server" {
		t.Errorf("expected name 'test-server', got %s", cfg.MCPServers[0].Name)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
}
