package main

import (
	"flag"
	"log"
	"net/http"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/user/openai-bridge/internal/config"
	"github.com/user/openai-bridge/internal/proxy"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if cfg.APIKey == "" {
		log.Printf("WARNING: api_key is not set — requests will have no Authorization header")
	} else {
		log.Printf("api_key loaded: %s", maskKey(cfg.APIKey))
	}

	clientOpts := []option.RequestOption{
		option.WithBaseURL(cfg.UpstreamBaseURL),
		option.WithMaxRetries(0),
	}
	if cfg.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(cfg.APIKey))
	}
	client := openai.NewClient(clientOpts...)

	srv := proxy.NewServer(&client, cfg.UpstreamBaseURL, cfg.APIKey, cfg.Debug, cfg.ModelMapping)

	log.Printf("openai-bridge listening on %s → %s", cfg.ListenAddr, cfg.UpstreamBaseURL)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// maskKey returns a redacted representation of an API key for safe logging.
func maskKey(key string) string {
	const show = 4
	if len(key) <= show*2 {
		return "[set, too short to display]"
	}
	return key[:show] + "..." + key[len(key)-show:]
}
