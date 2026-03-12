package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// newPassthroughHandler returns an http.Handler that reverse-proxies any
// request to the upstream OpenAI-compatible API unchanged.
//
// The incoming path is forwarded as-is, prefixed only by the path component of
// upstreamBaseURL (if any).  For example, if upstreamBaseURL is
// "http://host:7816" and the incoming path is "/v1/models", the forwarded path
// is "/v1/models".
func newPassthroughHandler(upstreamBaseURL, apiKey string) http.Handler {
	target, err := url.Parse(upstreamBaseURL)
	if err != nil {
		panic("invalid upstream_base_url: " + err.Error())
	}
	// Normalise: remove any trailing slash from the upstream path.
	target.Path = strings.TrimRight(target.Path, "/")

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host

			// Forward the path as-is, only prepending the upstream base path.
			// Example: upstream = "http://host:7816", incoming path = "/v1/models"
			//  → forwarded path = "/v1/models"
			req.URL.Path = target.Path + req.URL.Path

			// Always use the configured API key; never forward the incoming key.
			if apiKey == "" {
				// No key configured — forward the client's own Authorization header.
				clientAuth := req.Header.Get("Authorization")
				if clientAuth == "" {
					log.Printf("passthrough → %s %s (no api_key configured, no client auth)", req.Method, req.URL)
				} else {
					log.Printf("passthrough → %s %s (no api_key configured, forwarding client auth)", req.Method, req.URL)
				}
			} else {
				req.Header.Set("Authorization", "Bearer "+apiKey)
				log.Printf("passthrough → %s %s (auth: Bearer %s...%s)",
					req.Method, req.URL,
					truncate(apiKey, 4), tail(apiKey, 4))
			}

			// Prevent leaking the originating IP.
			req.Header.Del("X-Forwarded-For")
			req.Header.Del("X-Real-Ip")

			// httputil.ReverseProxy will set the Host header to req.URL.Host
			// automatically when req.Host == "".
			req.Host = ""
		},
		// Don't modify response — forward it byte-for-byte including streaming bodies.
		ModifyResponse: nil,
	}

	return proxy
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
