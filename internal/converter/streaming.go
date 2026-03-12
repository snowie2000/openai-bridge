package converter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// StreamResponses reads ChatCompletionChunks from stream, translates them into
// Responses API SSE events, and writes them to w.
//
// The caller must NOT pre-write any headers; this function sets
// Content-Type and Cache-Control itself.
func StreamResponses(
	w http.ResponseWriter,
	stream *ssestream.Stream[openai.ChatCompletionChunk],
	origReq *ResponsesRequest,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by server", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ss := &streamingSession{
		w:       w,
		flusher: flusher,
		origReq: origReq,
	}

	ss.run(stream)
}

// ---- SSE event shapes -------------------------------------------------------

// seqEvent is the base SSE envelope carrying a sequence_number.
type seqEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
}

// ---- Internal streaming state -----------------------------------------------

type streamingSession struct {
	w       http.ResponseWriter
	flusher http.Flusher
	origReq *ResponsesRequest
	seq     int

	// Filled from first chunk.
	chunkID string
	model   string
	created int64

	// Whether the message output item (index 0) has been opened.
	msgItemOpened bool
	// Whether the text content part (within the message item) has been opened.
	textPartOpened bool
	// Whether the refusal content part has been opened.
	refusalPartOpened bool
	// Whether any output item has been opened (used for nextOutputIndex tracking).
	nextOutputIndex int

	// Per-tool-call streaming state keyed by delta.Index.
	toolStates map[int]*streamingToolCall

	acc openai.ChatCompletionAccumulator
}

type streamingToolCall struct {
	outputIndex int
	itemID      string
	callID      string
	name        string
	argsBuilder strings.Builder
}

func (ss *streamingSession) nextSeq() int {
	ss.seq++
	return ss.seq
}

func (ss *streamingSession) writeEvent(payload any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(ss.w, "data: %s\n\n", data)
	ss.flusher.Flush()
}

func (ss *streamingSession) writeDone() {
	fmt.Fprintf(ss.w, "data: [DONE]\n\n")
	ss.flusher.Flush()
}

// ---- Partial / complete response builders -----------------------------------

func (ss *streamingSession) partialResponse(status string) map[string]any {
	tools := ss.origReq.Tools
	if tools == nil {
		tools = []ResponsesTool{}
	}

	var toolChoice interface{} = "auto"
	if len(ss.origReq.ToolChoice) > 0 && string(ss.origReq.ToolChoice) != "null" {
		var tc interface{}
		_ = json.Unmarshal(ss.origReq.ToolChoice, &tc)
		toolChoice = tc
	}

	parallelToolCalls := true
	if ss.origReq.ParallelToolCalls != nil {
		parallelToolCalls = *ss.origReq.ParallelToolCalls
	}

	r := map[string]any{
		"id":                  "resp_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
		"object":              "response",
		"created_at":          ss.created,
		"status":              status,
		"model":               ss.model,
		"output":              []any{},
		"tools":               tools,
		"tool_choice":         toolChoice,
		"parallel_tool_calls": parallelToolCalls,
		"temperature":         ss.origReq.Temperature,
		"top_p":               ss.origReq.TopP,
		"max_output_tokens":   ss.origReq.MaxOutputTokens,
		"store":               ss.origReq.Store,
		"metadata":            ss.origReq.Metadata,
	}
	if ss.origReq.Reasoning != nil {
		r["reasoning"] = ss.origReq.Reasoning
	}
	if ss.origReq.Text != nil {
		r["text"] = ss.origReq.Text
	}
	return r
}

func (ss *streamingSession) completedResponse() map[string]any {
	r := ss.partialResponse("completed")

	comp := &ss.acc.ChatCompletion
	now := time.Now().Unix()
	r["completed_at"] = now

	// Status from finish_reason.
	if len(comp.Choices) > 0 {
		switch comp.Choices[0].FinishReason {
		case "length":
			r["status"] = "incomplete"
			r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
			delete(r, "completed_at")
		case "content_filter":
			r["status"] = "failed"
			r["error"] = map[string]any{
				"code":    "content_filter",
				"message": "Response was blocked by content moderation",
			}
			delete(r, "completed_at")
		}
	}

	// Re-build output from accumulator.
	suffix := strings.TrimPrefix(comp.ID, "chatcmpl-")
	var output []any

	if len(comp.Choices) > 0 {
		choice := comp.Choices[0]
		var msgContent []any
		outputText := ""
		if choice.Message.Content != "" {
			msgContent = append(msgContent, map[string]any{
				"type":        "output_text",
				"text":        choice.Message.Content,
				"annotations": []any{},
			})
			outputText = choice.Message.Content
		}
		if choice.Message.Refusal != "" {
			msgContent = append(msgContent, map[string]any{
				"type":    "refusal",
				"refusal": choice.Message.Refusal,
			})
		}
		if len(msgContent) > 0 || len(choice.Message.ToolCalls) == 0 {
			msgItem := map[string]any{
				"id":      "msg_" + suffix,
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": msgContent,
			}
			if msgContent == nil {
				msgItem["content"] = []any{}
			}
			output = append(output, msgItem)
			if outputText != "" {
				r["output_text"] = outputText
			}
		}
		for i, tc := range choice.Message.ToolCalls {
			output = append(output, map[string]any{
				"id":        fmt.Sprintf("fc_%s_%d", suffix, i),
				"type":      "function_call",
				"status":    "completed",
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})
		}
	}

	if output == nil {
		output = []any{}
	}
	r["output"] = output

	// Usage.
	if comp.JSON.Usage.Valid() {
		r["usage"] = map[string]any{
			"input_tokens": comp.Usage.PromptTokens,
			"input_tokens_details": map[string]any{
				"cached_tokens": comp.Usage.PromptTokensDetails.CachedTokens,
			},
			"output_tokens": comp.Usage.CompletionTokens,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": comp.Usage.CompletionTokensDetails.ReasoningTokens,
			},
			"total_tokens": comp.Usage.TotalTokens,
		}
	}

	r["id"] = "resp_" + suffix
	r["model"] = comp.Model
	r["service_tier"] = string(comp.ServiceTier)

	return r
}

// ---- Main streaming loop ----------------------------------------------------

func (ss *streamingSession) run(stream *ssestream.Stream[openai.ChatCompletionChunk]) {
	// Emit prologue events once we have the first chunk.
	prologueSent := false

	for stream.Next() {
		chunk := stream.Current()
		ss.acc.AddChunk(chunk)

		if !prologueSent {
			ss.chunkID = chunk.ID
			ss.model = chunk.Model
			ss.created = chunk.Created
			ss.toolStates = make(map[int]*streamingToolCall)
			ss.nextOutputIndex = 0
			prologueSent = true

			partial := ss.partialResponse("in_progress")
			ss.writeEvent(ss.withSeq("response.created", map[string]any{"response": partial}))
			ss.writeEvent(ss.withSeq("response.in_progress", map[string]any{"response": partial}))
		}

		if len(chunk.Choices) == 0 {
			// Usage-only chunk.
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// ---- Text delta --------------------------------------------------------
		if delta.JSON.Content.Valid() && delta.Content != "" {
			ss.ensureMsgItemOpened()
			ss.ensureTextPartOpened()
			ss.writeEvent(ss.withSeq("response.output_text.delta", map[string]any{
				"item_id":       "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
				"output_index":  0,
				"content_index": 0,
				"delta":         delta.Content,
			}))
		}

		// ---- Refusal delta -----------------------------------------------------
		if delta.JSON.Refusal.Valid() && delta.Refusal != "" {
			ss.ensureMsgItemOpened()
			if !ss.refusalPartOpened {
				ss.refusalPartOpened = true
				ss.writeEvent(ss.withSeq("response.content_part.added", map[string]any{
					"item_id":       "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
					"output_index":  0,
					"content_index": 0,
					"part":          map[string]any{"type": "refusal", "refusal": ""},
				}))
			}
			ss.writeEvent(ss.withSeq("response.refusal.delta", map[string]any{
				"item_id":       "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
				"output_index":  0,
				"content_index": 0,
				"delta":         delta.Refusal,
			}))
		}

		// ---- Tool-call deltas --------------------------------------------------
		for _, tc := range delta.ToolCalls {
			idx := int(tc.Index)
			state, exists := ss.toolStates[idx]
			if !exists {
				// New tool call: open an output item.
				// Tool call items start after the message item (if any).
				outputIdx := ss.nextOutputIndex
				if ss.msgItemOpened {
					// Message item has output_index 0; tool calls start at 1.
					if outputIdx == 0 {
						outputIdx = 1
					}
				}
				// Each new tool state gets the next available index.
				outputIdx = ss.countToolCalls()
				if ss.msgItemOpened {
					outputIdx++ // account for message item at index 0
				}

				state = &streamingToolCall{
					outputIndex: outputIdx,
					itemID:      fmt.Sprintf("fc_%s_%d", strings.TrimPrefix(ss.chunkID, "chatcmpl-"), idx),
					callID:      tc.ID,
					name:        tc.Function.Name,
				}
				ss.toolStates[idx] = state

				ss.writeEvent(ss.withSeq("response.output_item.added", map[string]any{
					"output_index": state.outputIndex,
					"item": map[string]any{
						"id":        state.itemID,
						"type":      "function_call",
						"status":    "in_progress",
						"call_id":   state.callID,
						"name":      state.name,
						"arguments": "",
					},
				}))
			}

			// Accumulate name if not yet known (first delta carries it).
			if state.name == "" && tc.Function.Name != "" {
				state.name = tc.Function.Name
			}
			if state.callID == "" && tc.ID != "" {
				state.callID = tc.ID
			}

			if tc.Function.Arguments != "" {
				state.argsBuilder.WriteString(tc.Function.Arguments)
				ss.writeEvent(ss.withSeq("response.function_call_arguments.delta", map[string]any{
					"item_id":      state.itemID,
					"output_index": state.outputIndex,
					"delta":        tc.Function.Arguments,
				}))
			}
		}

		// ---- Finish reason handling -------------------------------------------
		switch choice.FinishReason {
		case "stop":
			ss.closeTextOutput()
			ss.closeMsgItem()

		case "tool_calls":
			// Close the message item if opened.
			ss.closeTextOutput()
			ss.closeMsgItem()
			// Close all open tool call items.
			ss.closeAllToolCalls()

		case "length":
			ss.closeTextOutput()
			ss.closeMsgItem()

		case "content_filter":
			ss.closeTextOutput()
			ss.closeMsgItem()
		}
	}

	// Check for stream error.
	if err := stream.Err(); err != nil {
		ss.writeEvent(ss.withSeq("error", map[string]any{
			"code":    "upstream_error",
			"message": err.Error(),
		}))
		ss.writeDone()
		return
	}

	// Emit response.completed with the full reconstructed response.
	ss.writeEvent(ss.withSeq("response.completed", map[string]any{
		"response": ss.completedResponse(),
	}))
	ss.writeDone()
}

// ---- Output item lifecycle helpers ------------------------------------------

func (ss *streamingSession) countToolCalls() int {
	return len(ss.toolStates)
}

func (ss *streamingSession) ensureMsgItemOpened() {
	if ss.msgItemOpened {
		return
	}
	ss.msgItemOpened = true
	if ss.nextOutputIndex == 0 {
		ss.nextOutputIndex = 1 // message item claims index 0
	}
	ss.writeEvent(ss.withSeq("response.output_item.added", map[string]any{
		"output_index": 0,
		"item": map[string]any{
			"id":      "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	}))
}

func (ss *streamingSession) ensureTextPartOpened() {
	if ss.textPartOpened {
		return
	}
	ss.textPartOpened = true
	ss.writeEvent(ss.withSeq("response.content_part.added", map[string]any{
		"item_id":       "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-"),
		"output_index":  0,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": ""},
	}))
}

func (ss *streamingSession) closeTextOutput() {
	if !ss.textPartOpened {
		return
	}
	msgID := "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-")
	ss.writeEvent(ss.withSeq("response.output_text.done", map[string]any{
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"text":          ss.acc.Choices[safeIdx(ss.acc.Choices)].Message.Content,
	}))
	ss.writeEvent(ss.withSeq("response.content_part.done", map[string]any{
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type": "output_text",
			"text": ss.acc.Choices[safeIdx(ss.acc.Choices)].Message.Content,
		},
	}))
}

func (ss *streamingSession) closeRefusalOutput() {
	if !ss.refusalPartOpened {
		return
	}
	msgID := "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-")
	refusal := ""
	if len(ss.acc.Choices) > 0 {
		refusal = ss.acc.Choices[0].Message.Refusal
	}
	ss.writeEvent(ss.withSeq("response.refusal.done", map[string]any{
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"refusal":       refusal,
	}))
	ss.writeEvent(ss.withSeq("response.content_part.done", map[string]any{
		"item_id":       msgID,
		"output_index":  0,
		"content_index": 0,
		"part":          map[string]any{"type": "refusal", "refusal": refusal},
	}))
}

func (ss *streamingSession) closeMsgItem() {
	if !ss.msgItemOpened {
		return
	}
	msgID := "msg_" + strings.TrimPrefix(ss.chunkID, "chatcmpl-")

	// Close refusal part if open.
	ss.closeRefusalOutput()

	// Build final content for the done event.
	var content []any
	if len(ss.acc.Choices) > 0 {
		if c := ss.acc.Choices[0].Message.Content; c != "" {
			content = append(content, map[string]any{
				"type":        "output_text",
				"text":        c,
				"annotations": []any{},
			})
		}
		if r := ss.acc.Choices[0].Message.Refusal; r != "" {
			content = append(content, map[string]any{
				"type":    "refusal",
				"refusal": r,
			})
		}
	}
	if content == nil {
		content = []any{}
	}

	ss.writeEvent(ss.withSeq("response.output_item.done", map[string]any{
		"output_index": 0,
		"item": map[string]any{
			"id":      msgID,
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": content,
		},
	}))
}

func (ss *streamingSession) closeAllToolCalls() {
	for _, state := range ss.toolStates {
		args := state.argsBuilder.String()
		ss.writeEvent(ss.withSeq("response.function_call_arguments.done", map[string]any{
			"item_id":      state.itemID,
			"output_index": state.outputIndex,
			"name":         state.name,
			"arguments":    args,
		}))
		ss.writeEvent(ss.withSeq("response.output_item.done", map[string]any{
			"output_index": state.outputIndex,
			"item": map[string]any{
				"id":        state.itemID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   state.callID,
				"name":      state.name,
				"arguments": args,
			},
		}))
	}
}

// withSeq wraps a payload map with the type and sequence_number fields.
func (ss *streamingSession) withSeq(eventType string, extra map[string]any) map[string]any {
	extra["type"] = eventType
	extra["sequence_number"] = ss.nextSeq()
	return extra
}

// safeIdx returns 0 if the slice is non-empty, so we can safely index into
// ss.acc.Choices without a bounds check inline.
func safeIdx(choices []openai.ChatCompletionChoice) int {
	if len(choices) == 0 {
		return 0
	}
	return 0
}
