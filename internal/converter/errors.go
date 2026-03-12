// Package converter implements the translation between the OpenAI Responses API
// wire format and the Chat Completions / Embeddings API wire format.
package converter

import (
	"encoding/json"
	"net/http"
)

// ---- Wire-format error types ------------------------------------------------

// APIError matches the OpenAI error object schema:
//
//	{"error": {"message": "...", "type": "...", "code": "...", "param": "..."}}
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

// NewAPIError builds an *APIErrorResponse suitable for returning as a 400 body.
func NewAPIError(code, message, param string) *APIErrorResponse {
	return &APIErrorResponse{
		Error: APIError{
			Message: message,
			Type:    "invalid_request_error",
			Code:    code,
			Param:   param,
		},
	}
}

// newAPIError is the package-internal alias for NewAPIError.
var newAPIError = NewAPIError

// WriteError encodes an APIErrorResponse as JSON and writes it with the given
// HTTP status code.
func WriteError(w http.ResponseWriter, statusCode int, e *APIErrorResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(e)
}

// WriteUpstreamError creates a generic APIErrorResponse from an arbitrary error
// and writes it with the given status code.
func WriteUpstreamError(w http.ResponseWriter, statusCode int, err error) {
	WriteError(w, statusCode, &APIErrorResponse{
		Error: APIError{
			Message: err.Error(),
			Type:    "upstream_error",
			Code:    "upstream_error",
		},
	})
}
