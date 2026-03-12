package converter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// ============================================================================
// Responses API — incoming request wire types
// ============================================================================

// ResponsesRequest is the JSON body of POST /v1/responses.
type ResponsesRequest struct {
	Model string         `json:"model"`
	Input ResponsesInput `json:"input"`

	Instructions *string `json:"instructions,omitempty"`

	// Translatable sampling / generation params.
	Temperature       *float64            `json:"temperature,omitempty"`
	TopP              *float64            `json:"top_p,omitempty"`
	MaxOutputTokens   *int64              `json:"max_output_tokens,omitempty"`
	Reasoning         *ResponsesReasoning `json:"reasoning,omitempty"`
	Text              *ResponsesText      `json:"text,omitempty"`
	Tools             []ResponsesTool     `json:"tools,omitempty"`
	ToolChoice        json.RawMessage     `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	Metadata          map[string]string   `json:"metadata,omitempty"`
	User              *string             `json:"user,omitempty"`
	SafetyIdentifier  *string             `json:"safety_identifier,omitempty"`
	Store             *bool               `json:"store,omitempty"`
	ServiceTier       string              `json:"service_tier,omitempty"`
	TopLogprobs       *int64              `json:"top_logprobs,omitempty"`
	Seed              *int64              `json:"seed,omitempty"`
	Stream            *bool               `json:"stream,omitempty"`

	// Unsupported — will return 400.
	PreviousResponseID *string         `json:"previous_response_id,omitempty"`
	Background         *bool           `json:"background,omitempty"`
	Prompt             json.RawMessage `json:"prompt,omitempty"`
	Conversation       json.RawMessage `json:"conversation,omitempty"`

	// Silently ignored.
	Truncation           string          `json:"truncation,omitempty"`
	ContextManagement    json.RawMessage `json:"context_management,omitempty"`
	Include              []string        `json:"include,omitempty"`
	StreamOptions        json.RawMessage `json:"stream_options,omitempty"`
	PromptCacheKey       *string         `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string          `json:"prompt_cache_retention,omitempty"`
}

// ResponsesInput is a union: either a plain string or an array of input items.
type ResponsesInput struct {
	IsString bool
	String   string
	Items    []ResponsesInputItem
}

func (r *ResponsesInput) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		r.IsString = true
		return json.Unmarshal(data, &r.String)
	}
	return json.Unmarshal(data, &r.Items)
}

// ResponsesInputItem is one element of a Responses API input array.
type ResponsesInputItem struct {
	// Common to all item types.
	Type string `json:"type"` // "message" | "function_call" | "tool_result"

	// Present on "message" items.
	Role    string           `json:"role,omitempty"`
	Content ResponsesContent `json:"content,omitempty"`

	// Present on "function_call" items (an assistant tool call turn).
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// Present on "tool_result" items.
	// CallID (same field) links back to the function_call.call_id.
	Output ResponsesContent `json:"output,omitempty"`
}

// ResponsesContent is a union: either a plain string or an array of content
// parts. Used for message.content and tool_result.output.
type ResponsesContent struct {
	IsEmpty  bool
	IsString bool
	String   string
	Parts    []ResponsesContentPart
}

func (r *ResponsesContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		r.IsEmpty = true
		return nil
	}
	if data[0] == '"' {
		r.IsString = true
		return json.Unmarshal(data, &r.String)
	}
	if data[0] == '[' {
		return json.Unmarshal(data, &r.Parts)
	}
	r.IsEmpty = true
	return nil
}

// ResponsesContentPart is one element of a content array.
type ResponsesContentPart struct {
	// type: "input_text" | "output_text" | "refusal" | "input_image" | "text" | "image"
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`

	// For input_image / image parts.
	ImageURL *ResponsesImageURL `json:"image_url,omitempty"`
}

// ResponsesImageURL holds the URL and optional detail level of an image.
type ResponsesImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ResponsesTool describes a tool in a Responses API request.
type ResponsesTool struct {
	// type: "function" (supported) | "file_search" | "web_search" |
	// "code_interpreter" | "computer_use" | "mcp" (all unsupported)
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ResponsesReasoning is the reasoning configuration.
type ResponsesReasoning struct {
	Effort          string `json:"effort,omitempty"`
	GenerateSummary string `json:"generate_summary,omitempty"`
	Summary         string `json:"summary,omitempty"`
}

// ResponsesText configures text output format and verbosity.
type ResponsesText struct {
	Format    *ResponsesTextFormat `json:"format,omitempty"`
	Verbosity string               `json:"verbosity,omitempty"`
}

// ResponsesTextFormat specifies the response format schema.
type ResponsesTextFormat struct {
	// type: "text" | "json_object" | "json_schema"
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ============================================================================
// Responses API — outgoing response wire types
// ============================================================================

// ResponsesResponse is the JSON body returned for POST /v1/responses (non-streaming).
type ResponsesResponse struct {
	ID     string `json:"id"`
	Object string `json:"object"` // always "response"

	CreatedAt   int64  `json:"created_at"`
	CompletedAt *int64 `json:"completed_at,omitempty"`
	FailedAt    *int64 `json:"failed_at,omitempty"`

	// "queued" | "in_progress" | "completed" | "incomplete" | "failed" | "cancelled"
	Status string `json:"status"`

	Error             *ResponsesRespError  `json:"error"`
	IncompleteDetails *ResponsesIncomplete `json:"incomplete_details"`

	Model string `json:"model"`

	Output     []ResponsesOutputItem `json:"output"`
	OutputText string                `json:"output_text,omitempty"`

	// Echoed from request.
	ParallelToolCalls bool                `json:"parallel_tool_calls"`
	Temperature       *float64            `json:"temperature"`
	TopP              *float64            `json:"top_p"`
	MaxOutputTokens   *int64              `json:"max_output_tokens"`
	ToolChoice        interface{}         `json:"tool_choice,omitempty"`
	Tools             []ResponsesTool     `json:"tools"`
	Reasoning         *ResponsesReasoning `json:"reasoning"`
	Text              *ResponsesText      `json:"text"`

	ServiceTier string            `json:"service_tier,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Store       *bool             `json:"store,omitempty"`
	Usage       *ResponsesUsage   `json:"usage,omitempty"`
}

// ResponsesRespError is the error field on a failed response.
type ResponsesRespError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResponsesIncomplete describes why a response was truncated.
type ResponsesIncomplete struct {
	Reason string `json:"reason"`
}

// ResponsesOutputItem is one element of the response output array.
type ResponsesOutputItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // "message" | "function_call"
	Status string `json:"status,omitempty"`

	// For type="message":
	Role    string                   `json:"role,omitempty"`
	Content []ResponsesOutputContent `json:"content,omitempty"`

	// For type="function_call":
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ResponsesOutputContent is one element of a message output content array.
type ResponsesOutputContent struct {
	// type: "output_text" | "refusal"
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Refusal     string `json:"refusal,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
}

// ResponsesUsage holds token usage for a completed response.
type ResponsesUsage struct {
	InputTokens         int64                       `json:"input_tokens"`
	InputTokensDetails  ResponsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokens        int64                       `json:"output_tokens"`
	OutputTokensDetails ResponsesOutputTokenDetails `json:"output_tokens_details"`
	TotalTokens         int64                       `json:"total_tokens"`
}

// ResponsesInputTokenDetails break down input token usage.
type ResponsesInputTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
	AudioTokens  int64 `json:"audio_tokens,omitempty"`
}

// ResponsesOutputTokenDetails break down output token usage.
type ResponsesOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
	AudioTokens     int64 `json:"audio_tokens,omitempty"`
}

// ============================================================================
// Request translation: Responses API → Chat Completions
// ============================================================================

// TranslateRequest converts a ResponsesRequest into openai.ChatCompletionNewParams.
// It returns an *APIErrorResponse (non-nil) when the request contains features
// that cannot be translated.
func TranslateRequest(req *ResponsesRequest) (openai.ChatCompletionNewParams, *APIErrorResponse) {
	// ---- Hard rejections -------------------------------------------------------

	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		return openai.ChatCompletionNewParams{}, newAPIError(
			"unsupported_feature",
			"previous_response_id requires server-side conversation state which this proxy does not support",
			"previous_response_id",
		)
	}
	if req.Conversation != nil && string(req.Conversation) != "null" && len(req.Conversation) > 0 {
		return openai.ChatCompletionNewParams{}, newAPIError(
			"unsupported_feature",
			"conversation state management is not supported by this proxy",
			"conversation",
		)
	}
	if req.Background != nil && *req.Background {
		return openai.ChatCompletionNewParams{}, newAPIError(
			"unsupported_feature",
			"background execution is not supported",
			"background",
		)
	}
	if req.Prompt != nil && len(req.Prompt) > 0 && string(req.Prompt) != "null" {
		return openai.ChatCompletionNewParams{}, newAPIError(
			"unsupported_feature",
			"prompt templates are not supported",
			"prompt",
		)
	}

	// Validate tools — only "function" type is translatable.
	for _, t := range req.Tools {
		if t.Type != "function" {
			return openai.ChatCompletionNewParams{}, newAPIError(
				"unsupported_tool_type",
				fmt.Sprintf("tool type %q cannot be translated to chat completions; only \"function\" tools are supported", t.Type),
				"tools",
			)
		}
	}

	// ---- Build messages --------------------------------------------------------

	var messages []openai.ChatCompletionMessageParamUnion

	// instructions → system message prepended before all input messages.
	if req.Instructions != nil && *req.Instructions != "" {
		messages = append(messages, openai.SystemMessage(*req.Instructions))
	}

	if req.Input.IsString {
		messages = append(messages, openai.UserMessage(req.Input.String))
	} else {
		for i, item := range req.Input.Items {
			msg, apiErr := translateInputItem(item)
			if apiErr != nil {
				apiErr.Error.Param = fmt.Sprintf("input[%d]", i)
				return openai.ChatCompletionNewParams{}, apiErr
			}
			messages = append(messages, msg)
		}
	}

	// ---- Build params ----------------------------------------------------------

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: messages,
	}

	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = openai.Float(*req.TopP)
	}
	if req.MaxOutputTokens != nil {
		params.MaxCompletionTokens = openai.Int(*req.MaxOutputTokens)
	}
	if req.TopLogprobs != nil {
		params.Logprobs = openai.Bool(true)
		params.TopLogprobs = openai.Int(*req.TopLogprobs)
	}
	if req.ParallelToolCalls != nil {
		params.ParallelToolCalls = openai.Bool(*req.ParallelToolCalls)
	}
	if req.Seed != nil {
		params.Seed = openai.Int(*req.Seed)
	}
	if req.User != nil {
		params.User = openai.String(*req.User)
	}
	if req.SafetyIdentifier != nil {
		params.SafetyIdentifier = openai.String(*req.SafetyIdentifier)
	}
	if req.Store != nil {
		params.Store = openai.Bool(*req.Store)
	}
	if req.ServiceTier != "" {
		params.ServiceTier = openai.ChatCompletionNewParamsServiceTier(req.ServiceTier)
	}
	if len(req.Metadata) > 0 {
		params.Metadata = shared.Metadata(req.Metadata)
	}

	// reasoning.effort → reasoning_effort
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(req.Reasoning.Effort)
	}

	// text.format → response_format; text.verbosity → verbosity
	if req.Text != nil {
		if req.Text.Format != nil {
			rf, apiErr := translateTextFormat(req.Text.Format)
			if apiErr != nil {
				return openai.ChatCompletionNewParams{}, apiErr
			}
			params.ResponseFormat = rf
		}
		if req.Text.Verbosity != "" {
			params.Verbosity = openai.ChatCompletionNewParamsVerbosity(req.Text.Verbosity)
		}
	}

	// tools
	if len(req.Tools) > 0 {
		tools, apiErr := translateTools(req.Tools)
		if apiErr != nil {
			return openai.ChatCompletionNewParams{}, apiErr
		}
		params.Tools = tools
	}

	// tool_choice
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		tc, apiErr := translateToolChoice(req.ToolChoice)
		if apiErr != nil {
			return openai.ChatCompletionNewParams{}, apiErr
		}
		params.ToolChoice = tc
	}

	return params, nil
}

// translateInputItem maps one Responses API input item to a Chat Completions
// message param.
func translateInputItem(item ResponsesInputItem) (openai.ChatCompletionMessageParamUnion, *APIErrorResponse) {
	// Clients that omit "type" but supply a "role" are sending chat-completions-
	// style message objects; treat them as "message" items.
	if item.Type == "" && item.Role != "" {
		item.Type = "message"
	}

	switch item.Type {
	case "message":
		return translateMessageItem(item)

	case "function_call":
		// An assistant turn that called a function.
		toolCall := openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: item.CallID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			},
		}
		return openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				ToolCalls: []openai.ChatCompletionMessageToolCallUnionParam{toolCall},
			},
		}, nil

	case "tool_result":
		text, apiErr := extractTextFromContent(item.Output, "tool_result.output")
		if apiErr != nil {
			return openai.ChatCompletionMessageParamUnion{}, apiErr
		}
		return openai.ToolMessage(text, item.CallID), nil

	default:
		return openai.ChatCompletionMessageParamUnion{}, newAPIError(
			"unsupported_input_type",
			fmt.Sprintf("input item type %q is not supported", item.Type),
			"input",
		)
	}
}

// translateMessageItem maps a Responses API message item to a Chat Completions
// message depending on role.
func translateMessageItem(item ResponsesInputItem) (openai.ChatCompletionMessageParamUnion, *APIErrorResponse) {
	role := strings.ToLower(item.Role)
	switch role {
	case "user":
		if item.Content.IsString {
			return openai.UserMessage(item.Content.String), nil
		}
		parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(item.Content.Parts))
		for _, p := range item.Content.Parts {
			switch p.Type {
			case "input_text", "text":
				parts = append(parts, openai.TextContentPart(p.Text))
			case "input_image", "image":
				if p.ImageURL == nil {
					return openai.ChatCompletionMessageParamUnion{}, newAPIError(
						"invalid_request_error",
						"input_image content part requires image_url",
						"input",
					)
				}
				parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL:    p.ImageURL.URL,
					Detail: p.ImageURL.Detail,
				}))
			default:
				return openai.ChatCompletionMessageParamUnion{}, newAPIError(
					"unsupported_content_type",
					fmt.Sprintf("user message content type %q is not supported", p.Type),
					"input",
				)
			}
		}
		return openai.UserMessage(parts), nil

	case "assistant":
		if item.Content.IsString {
			return openai.AssistantMessage(item.Content.String), nil
		}
		var texts []string
		var refusal string
		for _, p := range item.Content.Parts {
			switch p.Type {
			case "output_text", "text":
				texts = append(texts, p.Text)
			case "refusal":
				refusal = p.Refusal
				// skip unknown types (e.g. reasoning, tool_use) — they have no Chat
				// Completions equivalent.
			}
		}
		ap := openai.ChatCompletionAssistantMessageParam{}
		if len(texts) > 0 {
			combined := strings.Join(texts, "")
			ap.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(combined),
			}
		}
		if refusal != "" {
			ap.Refusal = openai.String(refusal)
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &ap}, nil

	case "system":
		if item.Content.IsString {
			return openai.SystemMessage(item.Content.String), nil
		}
		return openai.SystemMessage(joinTextParts(item.Content.Parts)), nil

	case "developer":
		if item.Content.IsString {
			return openai.DeveloperMessage(item.Content.String), nil
		}
		return openai.DeveloperMessage(joinTextParts(item.Content.Parts)), nil

	default:
		return openai.ChatCompletionMessageParamUnion{}, newAPIError(
			"unsupported_input_type",
			fmt.Sprintf("message role %q is not supported", item.Role),
			"input",
		)
	}
}

// extractTextFromContent returns the text of a ResponsesContent, returning a
// 400 error if it contains non-text parts (e.g. images).
func extractTextFromContent(c ResponsesContent, fieldName string) (string, *APIErrorResponse) {
	if c.IsEmpty {
		return "", nil
	}
	if c.IsString {
		return c.String, nil
	}
	var parts []string
	for _, p := range c.Parts {
		switch p.Type {
		case "text", "output_text", "input_text":
			parts = append(parts, p.Text)
		case "image", "input_image":
			return "", newAPIError(
				"unsupported_content_type",
				fmt.Sprintf("image content in %s is not supported by chat completions", fieldName),
				fieldName,
			)
		default:
			return "", newAPIError(
				"unsupported_content_type",
				fmt.Sprintf("content type %q in %s is not supported", p.Type, fieldName),
				fieldName,
			)
		}
	}
	return strings.Join(parts, ""), nil
}

// joinTextParts concatenates text from content parts that carry text.
func joinTextParts(parts []ResponsesContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// translateTextFormat maps ResponsesTextFormat to a ChatCompletionNewParams
// ResponseFormat union.
func translateTextFormat(f *ResponsesTextFormat) (openai.ChatCompletionNewParamsResponseFormatUnion, *APIErrorResponse) {
	switch f.Type {
	case "text", "":
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfText: &shared.ResponseFormatTextParam{},
		}, nil

	case "json_object":
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		}, nil

	case "json_schema":
		if f.Name == "" {
			return openai.ChatCompletionNewParamsResponseFormatUnion{}, newAPIError(
				"invalid_request_error",
				"text.format.name is required when text.format.type is \"json_schema\"",
				"text.format.name",
			)
		}
		jp := shared.ResponseFormatJSONSchemaJSONSchemaParam{Name: f.Name}
		if f.Description != "" {
			jp.Description = openai.String(f.Description)
		}
		if f.Strict != nil {
			jp.Strict = openai.Bool(*f.Strict)
		}
		if len(f.Schema) > 0 && string(f.Schema) != "null" {
			var schema any
			if err := json.Unmarshal(f.Schema, &schema); err != nil {
				return openai.ChatCompletionNewParamsResponseFormatUnion{}, newAPIError(
					"invalid_request_error",
					"text.format.schema is not valid JSON",
					"text.format.schema",
				)
			}
			jp.Schema = schema
		}
		return openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{JSONSchema: jp},
		}, nil

	default:
		return openai.ChatCompletionNewParamsResponseFormatUnion{}, newAPIError(
			"invalid_request_error",
			fmt.Sprintf("unknown text.format.type %q; expected \"text\", \"json_object\", or \"json_schema\"", f.Type),
			"text.format.type",
		)
	}
}

// translateTools converts Responses API tool definitions to Chat Completions
// tool params. Only "function" type is supported.
func translateTools(tools []ResponsesTool) ([]openai.ChatCompletionToolUnionParam, *APIErrorResponse) {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		fd := shared.FunctionDefinitionParam{Name: t.Name}
		if t.Description != "" {
			fd.Description = openai.String(t.Description)
		}
		if t.Strict != nil {
			fd.Strict = openai.Bool(*t.Strict)
		}
		if len(t.Parameters) > 0 && string(t.Parameters) != "null" {
			var params shared.FunctionParameters
			if err := json.Unmarshal(t.Parameters, &params); err != nil {
				return nil, newAPIError(
					"invalid_request_error",
					fmt.Sprintf("invalid parameters JSON for tool %q", t.Name),
					"tools",
				)
			}
			fd.Parameters = params
		}
		result = append(result, openai.ChatCompletionFunctionTool(fd))
	}
	return result, nil
}

// translateToolChoice maps a Responses API tool_choice value (string or object)
// to a Chat Completions ToolChoiceOptionUnionParam.
func translateToolChoice(raw json.RawMessage) (openai.ChatCompletionToolChoiceOptionUnionParam, *APIErrorResponse) {
	// String: "auto" | "none" | "required"
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "none", "required":
			return openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String(s),
			}, nil
		default:
			return openai.ChatCompletionToolChoiceOptionUnionParam{}, newAPIError(
				"invalid_request_error",
				fmt.Sprintf("unsupported tool_choice string %q; expected \"auto\", \"none\", or \"required\"", s),
				"tool_choice",
			)
		}
	}

	// Object: {"type": "function", "name": "..."}
	// (Responses API uses {type, name}; Chat Completions uses {type, function:{name}})
	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name"` // Responses API style
		Function *struct {
			Name string `json:"name"`
		} `json:"function"` // Chat Completions style (also accepted)
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return openai.ChatCompletionToolChoiceOptionUnionParam{}, newAPIError(
			"invalid_request_error",
			"could not parse tool_choice object",
			"tool_choice",
		)
	}

	switch obj.Type {
	case "function":
		name := obj.Name
		if name == "" && obj.Function != nil {
			name = obj.Function.Name
		}
		if name == "" {
			return openai.ChatCompletionToolChoiceOptionUnionParam{}, newAPIError(
				"invalid_request_error",
				"tool_choice.name is required when tool_choice.type is \"function\"",
				"tool_choice.name",
			)
		}
		return openai.ToolChoiceOptionFunctionToolChoice(
			openai.ChatCompletionNamedToolChoiceFunctionParam{Name: name},
		), nil

	default:
		return openai.ChatCompletionToolChoiceOptionUnionParam{}, newAPIError(
			"unsupported_tool_type",
			fmt.Sprintf("tool_choice type %q is not supported; only \"function\" is supported", obj.Type),
			"tool_choice.type",
		)
	}
}

// ============================================================================
// Response translation: Chat Completions → Responses API
// ============================================================================

// TranslateResponse converts an openai.ChatCompletion into a ResponsesResponse.
func TranslateResponse(comp *openai.ChatCompletion, origReq *ResponsesRequest) *ResponsesResponse {
	now := time.Now().Unix()

	resp := &ResponsesResponse{
		ID:        "resp_" + strings.TrimPrefix(comp.ID, "chatcmpl-"),
		Object:    "response",
		CreatedAt: comp.Created,
		Model:     comp.Model,
		Tools:     origReq.Tools,
		// These are nil by default; set below.
		Error:             nil,
		IncompleteDetails: nil,
	}

	if resp.Tools == nil {
		resp.Tools = []ResponsesTool{}
	}

	// Echo sampling params from request.
	resp.Temperature = origReq.Temperature
	resp.TopP = origReq.TopP
	resp.MaxOutputTokens = origReq.MaxOutputTokens
	resp.Store = origReq.Store
	resp.Metadata = origReq.Metadata
	resp.Reasoning = origReq.Reasoning
	resp.Text = origReq.Text

	if origReq.ParallelToolCalls != nil {
		resp.ParallelToolCalls = *origReq.ParallelToolCalls
	} else {
		resp.ParallelToolCalls = true
	}

	// Echo tool_choice: decode to an interface{} so it serialises correctly.
	if len(origReq.ToolChoice) > 0 && string(origReq.ToolChoice) != "null" {
		var tc interface{}
		_ = json.Unmarshal(origReq.ToolChoice, &tc)
		resp.ToolChoice = tc
	} else {
		resp.ToolChoice = "auto"
	}

	// Service tier from response.
	resp.ServiceTier = string(comp.ServiceTier)

	// Usage.
	if comp.JSON.Usage.Valid() {
		resp.Usage = &ResponsesUsage{
			InputTokens: comp.Usage.PromptTokens,
			InputTokensDetails: ResponsesInputTokenDetails{
				CachedTokens: comp.Usage.PromptTokensDetails.CachedTokens,
				AudioTokens:  comp.Usage.PromptTokensDetails.AudioTokens,
			},
			OutputTokens: comp.Usage.CompletionTokens,
			OutputTokensDetails: ResponsesOutputTokenDetails{
				ReasoningTokens: comp.Usage.CompletionTokensDetails.ReasoningTokens,
				AudioTokens:     comp.Usage.CompletionTokensDetails.AudioTokens,
			},
			TotalTokens: comp.Usage.TotalTokens,
		}
	}

	// Process choices.
	if len(comp.Choices) == 0 {
		resp.Status = "incomplete"
		resp.IncompleteDetails = &ResponsesIncomplete{Reason: "no_choices"}
		resp.Output = []ResponsesOutputItem{}
		return resp
	}

	choice := comp.Choices[0]

	switch choice.FinishReason {
	case "stop":
		resp.Status = "completed"
		resp.CompletedAt = &now
	case "tool_calls", "function_call":
		resp.Status = "completed"
		resp.CompletedAt = &now
	case "length":
		resp.Status = "incomplete"
		resp.IncompleteDetails = &ResponsesIncomplete{Reason: "max_output_tokens"}
	case "content_filter":
		resp.Status = "failed"
		resp.FailedAt = &now
		resp.Error = &ResponsesRespError{
			Code:    "content_filter",
			Message: "Response was blocked by content moderation",
		}
	default:
		resp.Status = "completed"
		resp.CompletedAt = &now
	}

	// Build output items.
	suffix := strings.TrimPrefix(comp.ID, "chatcmpl-")

	var outputItems []ResponsesOutputItem
	var outputText string

	// Message item (text content and/or refusal).
	var msgContent []ResponsesOutputContent
	if choice.Message.Content != "" {
		msgContent = append(msgContent, ResponsesOutputContent{
			Type:        "output_text",
			Text:        choice.Message.Content,
			Annotations: []any{},
		})
		outputText = choice.Message.Content
	}
	if choice.Message.Refusal != "" {
		msgContent = append(msgContent, ResponsesOutputContent{
			Type:    "refusal",
			Refusal: choice.Message.Refusal,
		})
	}
	if len(msgContent) > 0 || len(choice.Message.ToolCalls) == 0 {
		outputItems = append(outputItems, ResponsesOutputItem{
			ID:      "msg_" + suffix,
			Type:    "message",
			Role:    "assistant",
			Status:  "completed",
			Content: msgContent,
		})
	}

	// Function-call output items.
	for i, tc := range choice.Message.ToolCalls {
		outputItems = append(outputItems, ResponsesOutputItem{
			ID:        fmt.Sprintf("fc_%s_%d", suffix, i),
			Type:      "function_call",
			Status:    "completed",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	resp.Output = outputItems
	resp.OutputText = outputText

	if resp.Output == nil {
		resp.Output = []ResponsesOutputItem{}
	}

	return resp
}
