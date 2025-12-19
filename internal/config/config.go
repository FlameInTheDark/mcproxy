package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"` // "stdio" or "sse"
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
	URL     string   `yaml:"url,omitempty"`
	Env     []string `yaml:"env,omitempty"`
}

type Config struct {
	MCPServers []ServerConfig `yaml:"mcpServers"`
	Server     struct {
		Port      int    `yaml:"port"`
		Transport string `yaml:"transport"` // "http" or "stdio"
	} `yaml:"server"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	decoder := yaml.NewDecoder(f)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Transport == "" {
		cfg.Server.Transport = "http"
	}

	return &cfg, nil
}
