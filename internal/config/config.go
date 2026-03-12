package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds all runtime configuration for openai-bridge.
type Config struct {
	// ListenAddr is the TCP address the proxy server listens on.
	// Defaults to ":8080".
	ListenAddr string `yaml:"listen_addr"`

	// UpstreamBaseURL is the OpenAI-compatible base URL including the /v1 path,
	// e.g. "https://api.openai.com/v1" or "https://api.groq.com/openai/v1".
	UpstreamBaseURL string `yaml:"upstream_base_url"`

	// APIKey is the API key forwarded to every upstream request.
	APIKey string `yaml:"api_key"`

	// Debug enables verbose request/response body logging.
	Debug bool `yaml:"debug"`

	// ModelMapping translates client-requested model names to upstream model names.
	// Key is the model name the client sends; value is what is forwarded upstream.
	ModelMapping map[string]string `yaml:"model_mapping"`
}

// LoadConfig reads and parses a YAML config file from path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.UpstreamBaseURL == "" {
		cfg.UpstreamBaseURL = "https://api.openai.com/v1"
	}

	return &cfg, nil
}
