package converter

import (
	"encoding/json"
	"net/http"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// EmbeddingsRequest is the wire shape for POST /v1/embeddings.
// It accepts both the old format (model+input) and the new format that the
// Responses API ecosystem also uses — the fields are identical.
type EmbeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     *int64          `json:"dimensions,omitempty"`
	User           string          `json:"user,omitempty"`
}

// HandleEmbeddings handles POST /v1/embeddings by translating the request into
// an openai-go EmbeddingNewParams call and returning the upstream response
// verbatim (it is already in the correct format for both old and new clients).
func HandleEmbeddings(w http.ResponseWriter, r *http.Request, client *openai.Client, mapModel func(string) string, opts ...option.RequestOption) {
	var req EmbeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, newAPIError("invalid_request_error", "Could not parse request body: "+err.Error(), ""))
		return
	}

	req.Model = mapModel(req.Model)

	if req.Model == "" {
		WriteError(w, http.StatusBadRequest, newAPIError("invalid_request_error", "Field 'model' is required", "model"))
		return
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		WriteError(w, http.StatusBadRequest, newAPIError("invalid_request_error", "Field 'input' is required", "input"))
		return
	}

	inputUnion, apiErr := translateEmbeddingsInput(req.Input)
	if apiErr != nil {
		WriteError(w, http.StatusBadRequest, apiErr)
		return
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(req.Model),
		Input: inputUnion,
	}

	if req.Dimensions != nil {
		params.Dimensions = param.NewOpt(*req.Dimensions)
	}

	switch req.EncodingFormat {
	case "float", "":
		params.EncodingFormat = openai.EmbeddingNewParamsEncodingFormatFloat
	case "base64":
		params.EncodingFormat = openai.EmbeddingNewParamsEncodingFormatBase64
	default:
		WriteError(w, http.StatusBadRequest, newAPIError("invalid_request_error",
			"Unknown encoding_format '"+req.EncodingFormat+"'. Must be 'float' or 'base64'", "encoding_format"))
		return
	}

	if req.User != "" {
		params.User = param.NewOpt(req.User)
	}

	resp, err := client.Embeddings.New(r.Context(), params, opts...)
	if err != nil {
		WriteUpstreamError(w, http.StatusBadGateway, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// translateEmbeddingsInput converts a raw JSON input value into the union type
// expected by openai-go.
func translateEmbeddingsInput(raw json.RawMessage) (openai.EmbeddingNewParamsInputUnion, *APIErrorResponse) {
	if len(raw) == 0 {
		return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "input is empty", "input")
	}

	switch raw[0] {
	case '"':
		// Single string.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "invalid string input: "+err.Error(), "input")
		}
		return openai.EmbeddingNewParamsInputUnion{OfString: param.NewOpt(s)}, nil

	case '[':
		// Array — peek at the first element to disambiguate.
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "invalid array input: "+err.Error(), "input")
		}
		if len(arr) == 0 {
			// Empty array — treat as array of strings.
			return openai.EmbeddingNewParamsInputUnion{
				OfArrayOfStrings: []string{},
			}, nil
		}

		first := arr[0]
		// Skip leading whitespace.
		firstTrimmed := first
		for len(firstTrimmed) > 0 && (firstTrimmed[0] == ' ' || firstTrimmed[0] == '\t' || firstTrimmed[0] == '\n' || firstTrimmed[0] == '\r') {
			firstTrimmed = firstTrimmed[1:]
		}
		if len(firstTrimmed) == 0 {
			return openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: []string{}}, nil
		}

		switch firstTrimmed[0] {
		case '"':
			// Array of strings.
			var strings []string
			if err := json.Unmarshal(raw, &strings); err != nil {
				return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "invalid array of strings: "+err.Error(), "input")
			}
			return openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: strings}, nil

		case '[':
			// Array of token arrays.
			var tokenArrays [][]int64
			if err := json.Unmarshal(raw, &tokenArrays); err != nil {
				return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "invalid array of token arrays: "+err.Error(), "input")
			}
			return openai.EmbeddingNewParamsInputUnion{OfArrayOfTokenArrays: tokenArrays}, nil

		default:
			// Number — array of tokens.
			var tokens []int64
			if err := json.Unmarshal(raw, &tokens); err != nil {
				return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error", "invalid array of tokens: "+err.Error(), "input")
			}
			return openai.EmbeddingNewParamsInputUnion{OfArrayOfTokens: tokens}, nil
		}

	default:
		return openai.EmbeddingNewParamsInputUnion{}, newAPIError("invalid_request_error",
			"input must be a string or array", "input")
	}
}
