package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/user/openai-bridge/internal/converter"
)

// responseWriter wraps http.ResponseWriter to capture the status code,
// number of bytes written, and the response body for debug logging.
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	body   bytes.Buffer
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Flush forwards the flush call to the underlying ResponseWriter if it
// supports it, so SSE streaming still works through the wrapper.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Server is the main HTTP handler for the bridge.
type Server struct {
	client       *openai.Client
	hasAPIKey    bool // true when a key is set in config
	debug        bool
	modelMapping map[string]string
	passthrough  http.Handler
}

// NewServer constructs a Server with a pre-built openai.Client and a
// passthrough handler for routes that are not translated.
func NewServer(client *openai.Client, upstreamBaseURL, apiKey string, debug bool, modelMapping map[string]string) *Server {
	return &Server{
		client:       client,
		hasAPIKey:    apiKey != "",
		debug:        debug,
		modelMapping: modelMapping,
		passthrough:  newPassthroughHandler(upstreamBaseURL, apiKey),
	}
}

// mapModel returns the upstream model name for a given client model name,
// falling back to the original name if no mapping is configured.
func (s *Server) mapModel(model string) string {
	if mapped, ok := s.modelMapping[model]; ok {
		log.Printf("model mapping: %q → %q", model, mapped)
		return mapped
	}
	return model
}

// clientAuthOpts returns a per-request option that forwards the incoming
// request's Bearer token to the upstream when no API key is configured.
func (s *Server) clientAuthOpts(r *http.Request) []option.RequestOption {
	if s.hasAPIKey {
		return nil
	}
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok && after != "" {
		return []option.RequestOption{option.WithAPIKey(after)}
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()

	defer func() {
		log.Printf("%s %s %d %dB %s",
			r.Method, r.RequestURI, rw.status, rw.bytes, time.Since(start).Round(time.Millisecond))
		if s.debug && rw.body.Len() > 0 {
			log.Printf("response body:\n%s", rw.body.String())
		}
	}()

	// Normalize: strip a leading /v1 prefix so the bridge works whether the
	// client treats it as the base URL (calls /responses) or includes the
	// prefix explicitly (calls /v1/responses).
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/") {
		path = path[3:] // "/v1/foo" → "/foo"
	} else if path == "/v1" {
		path = "/"
	}

	switch {
	case r.Method == http.MethodPost && path == "/responses":
		s.handleResponses(rw, r)
	case r.Method == http.MethodPost && path == "/embeddings":
		converter.HandleEmbeddings(rw, r, s.client, s.mapModel, s.clientAuthOpts(r)...)
	default:
		log.Printf("router: no match for %s %q — falling through to passthrough", r.Method, r.URL.Path)
		s.passthrough.ServeHTTP(rw, r)
	}
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		converter.WriteError(w, http.StatusBadRequest,
			converter.NewAPIError("invalid_request_error", "Could not read request body: "+err.Error(), ""))
		return
	}
	if s.debug {
		log.Printf("request body:\n%s", string(rawBody))
	}
	r.Body = io.NopCloser(bytes.NewReader(rawBody))

	var req converter.ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		converter.WriteError(w, http.StatusBadRequest,
			converter.NewAPIError("invalid_request_error", "Could not parse request body: "+err.Error(), ""))
		return
	}

	req.Model = s.mapModel(req.Model)

	params, apiErr := converter.TranslateRequest(&req)
	if apiErr != nil {
		converter.WriteError(w, http.StatusBadRequest, apiErr)
		return
	}

	ctx := r.Context()

	authOpts := s.clientAuthOpts(r)

	if req.Stream != nil && *req.Stream {
		stream := s.client.Chat.Completions.NewStreaming(ctx, params, authOpts...)
		converter.StreamResponses(w, stream, &req)
		return
	}

	comp, err := s.client.Chat.Completions.New(ctx, params, authOpts...)
	if err != nil {
		converter.WriteUpstreamError(w, http.StatusBadGateway, err)
		return
	}

	resp := converter.TranslateResponse(comp, &req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
