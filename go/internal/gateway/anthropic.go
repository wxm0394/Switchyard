// Anthropic-compatible Messages API (POST /v1/messages) for clients
// that speak Anthropic format — Claude Code in particular. The
// gateway itself only routes OpenAI-format requests, so this file
// translates Anthropic <-> OpenAI at the edge and reuses the shared
// serveChat pipeline (routing, failover, audit, cost, auto-enroll)
// unchanged.
//
// Wire notes (Anthropic Messages API, as Claude Code uses it):
//   - Auth: x-api-key (what Claude Code sends) or Authorization:
//     Bearer. The value is a Switchyard client token (sk-portal-*)
//     either way; the token is looked up exactly like the OpenAI path.
//   - Model names map to Switchyard aliases: claude-opus-* ->
//     reasoning, claude-haiku-* -> fast, claude-sonnet-* and other
//     claude-* -> coder. Literal alias names (coder / reasoning /
//     fast / vision / act) and exact upstream modelIds pass through
//     unchanged, so a direct route pin still works.
//   - Streaming is translated event-by-event from the buffered OpenAI
//     SSE body the gateway already returns. Usage comes from the
//     stream's usage chunk when the upstream sends one
//     (stream_options.include_usage is set on the translated request),
//     otherwise from the gateway's prompt estimate; message_delta
//     carries the final usage, which is what clients bill on.
//   - Dropped on the floor, deliberately: cache_control (no prompt
//     caching in the gateway), thinking / redacted_thinking blocks
//     (the backing OpenAI models have no equivalent), metadata and
//     service_tier. Unknown fields are ignored, never rejected.
package gateway

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---- Anthropic request types ----

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// image
	Source *anthropicImageSource `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	System        json.RawMessage      `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	Tools         []anthropicTool      `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream"`
}

// ---- handler ----

// HandleAnthropicMessages serves POST /v1/messages (and
// /api/v1/messages). Auth and validation errors use the Anthropic
// error shape; the request is translated to OpenAI shape and handed
// to the shared serveChat pipeline with format "anthropic".
func (g *Gateway) HandleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		anthropicError(w, 400, "failed to read body", "invalid_request_error")
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		anthropicError(w, 400, "invalid JSON body", "invalid_request_error")
		return
	}
	// Auth: x-api-key first (Claude Code), Bearer as fallback. The
	// value is a Switchyard client token either way.
	key := strings.TrimSpace(r.Header.Get("x-api-key"))
	if key == "" {
		key = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	token, err := g.Store.GetTokenByValue(key)
	if err != nil || token == nil {
		anthropicError(w, 401, "Invalid or missing API key. Use a valid Switchyard client token (sk-portal-*) as the x-api-key.", "authentication_error")
		return
	}
	if !token.Active {
		anthropicError(w, 403, fmt.Sprintf("Token %q is currently disabled.", token.Name), "permission_error")
		return
	}
	if token.QuotaLimit != nil && token.UsedTokens >= *token.QuotaLimit {
		anthropicError(w, 429, fmt.Sprintf("Token %q has reached its token quota limit (%d / %d).", token.Name, token.UsedTokens, *token.QuotaLimit), "rate_limit_error")
		return
	}
	raw, openAIBody, displayModel, err := translateAnthropicRequest(&req)
	if err != nil {
		anthropicError(w, 400, err.Error(), "invalid_request_error")
		return
	}
	g.serveChat(w, r, raw, openAIBody, token, displayModel, "anthropic")
}

// anthropicError writes {"type":"error","error":{"type":...,"message":...}}.
func anthropicError(w http.ResponseWriter, status int, message, typ string) {
	writeJSON(w, status, map[string]interface{}{
		"type":  "error",
		"error": map[string]interface{}{"type": typ, "message": message},
	})
}

// anthropicErrorType maps an HTTP status to an Anthropic error type.
func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 429:
		return "rate_limit_error"
	case 503:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func newAnthropicMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("msg_%x", b)
}

// ---- request translation: Anthropic -> OpenAI ----

// anthropicModelToAlias maps the model name a Claude Code-style
// client sends to the Switchyard pool that should serve it.
func anthropicModelToAlias(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch m {
	case "coder", "reasoning", "fast", "vision", "act":
		return m
	}
	if strings.Contains(m, "opus") {
		return "reasoning"
	}
	if strings.Contains(m, "haiku") {
		return "fast"
	}
	if strings.HasPrefix(m, "claude-") || strings.Contains(m, "sonnet") {
		return "coder"
	}
	return model
}

// anthropicSystemText flattens the system prompt (string, or a list
// of text blocks) to plain text. Returns "" when absent.
func anthropicSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := []string{}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// decodeAnthropicContent splits a message content field into its
// string form or its block list.
func decodeAnthropicContent(raw json.RawMessage) (string, []anthropicContentBlock, bool) {
	if len(raw) == 0 {
		return "", nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, false
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, false
	}
	return "", blocks, true
}

// anthropicBlockText flattens a tool_result content field (string or
// blocks) to plain text. Images inside a tool result have no OpenAI
// tool-message equivalent and become a placeholder.
func anthropicBlockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	parts := []string{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		default:
			if len(b.Content) > 0 {
				parts = append(parts, anthropicBlockText(b.Content))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// translateAnthropicMessages converts Anthropic messages to OpenAI
// messages. Assistant tool_use blocks become tool_calls, user
// tool_result blocks become role=tool messages (in block order, so a
// tool result still follows the assistant turn that produced it),
// images become image_url parts. Thinking blocks are dropped; a
// message left with nothing usable is dropped with them.
func translateAnthropicMessages(in []anthropicMessage) []interface{} {
	out := []interface{}{}
	for _, m := range in {
		text, blocks, isBlocks := decodeAnthropicContent(m.Content)
		if !isBlocks {
			out = append(out, map[string]interface{}{"role": m.Role, "content": text})
			continue
		}
		switch m.Role {
		case "assistant":
			var textParts []string
			var toolCalls []interface{}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				case "tool_use":
					args := "{}"
					if len(b.Input) > 0 {
						args = string(b.Input)
					}
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   b.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      b.Name,
							"arguments": args,
						},
					})
				}
			}
			if len(textParts) == 0 && len(toolCalls) == 0 {
				continue
			}
			msg := map[string]interface{}{"role": "assistant"}
			if len(textParts) > 0 {
				msg["content"] = strings.Join(textParts, "\n")
			} else {
				msg["content"] = nil
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		default:
			// User (or anything else): consecutive text/image blocks
			// stay in one message; each tool_result splits out as
			// its own role=tool message, in block order.
			var parts []interface{}
			flush := func() {
				if len(parts) > 0 {
					out = append(out, map[string]interface{}{"role": "user", "content": parts})
					parts = nil
				}
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					parts = append(parts, map[string]interface{}{"type": "text", "text": b.Text})
				case "image":
					if b.Source != nil {
						url := ""
						switch b.Source.Type {
						case "base64":
							url = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
						case "url":
							url = b.Source.URL
						}
						if url != "" {
							parts = append(parts, map[string]interface{}{
								"type":      "image_url",
								"image_url": map[string]interface{}{"url": url},
							})
						}
					}
				case "tool_result":
					flush()
					out = append(out, map[string]interface{}{
						"role":         "tool",
						"tool_call_id": b.ToolUseID,
						"content":      anthropicBlockText(b.Content),
					})
				}
			}
			flush()
		}
	}
	return out
}

// translateAnthropicTools converts Anthropic tool definitions to
// OpenAI function tools. input_schema passes through as parameters.
func translateAnthropicTools(in []anthropicTool) []interface{} {
	out := []interface{}{}
	for _, t := range in {
		if t.Name == "" {
			continue
		}
		params := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		if len(t.InputSchema) > 0 {
			var p map[string]interface{}
			if err := json.Unmarshal(t.InputSchema, &p); err == nil && p != nil {
				params = p
			}
		}
		if _, ok := params["type"]; !ok {
			params["type"] = "object"
		}
		out = append(out, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// translateAnthropicToolChoice maps Anthropic tool_choice to the
// OpenAI equivalent. Returns nil when the request sets none.
func translateAnthropicToolChoice(tc *anthropicToolChoice) interface{} {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if tc.Name != "" {
			return map[string]interface{}{
				"type":     "function",
				"function": map[string]interface{}{"name": tc.Name},
			}
		}
		return "auto"
	default:
		return "auto"
	}
}

// translateAnthropicRequest converts a whole Anthropic Messages
// request into the OpenAI-shaped raw map + body bytes the shared
// pipeline consumes. Returns the display model (the original Claude
// model name, echoed back in responses) alongside.
func translateAnthropicRequest(req *anthropicRequest) (map[string]interface{}, []byte, string, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, nil, "", fmt.Errorf("model is required")
	}
	if len(req.Messages) == 0 {
		return nil, nil, "", fmt.Errorf("messages must not be empty")
	}
	displayModel := req.Model
	msgs := translateAnthropicMessages(req.Messages)
	if len(msgs) == 0 {
		return nil, nil, "", fmt.Errorf("messages must contain at least one usable message")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	raw := map[string]interface{}{
		"model":      anthropicModelToAlias(req.Model),
		"messages":   msgs,
		"max_tokens": maxTokens,
		"stream":     req.Stream,
	}
	if sys := anthropicSystemText(req.System); sys != "" {
		all := []interface{}{map[string]interface{}{"role": "system", "content": sys}}
		all = append(all, msgs...)
		raw["messages"] = all
	}
	if tools := translateAnthropicTools(req.Tools); len(tools) > 0 {
		raw["tools"] = tools
		if tc := translateAnthropicToolChoice(req.ToolChoice); tc != nil {
			raw["tool_choice"] = tc
		}
	}
	if req.Temperature != nil {
		raw["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		raw["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		raw["stop"] = req.StopSequences
	}
	if req.Stream {
		// Ask the upstream to report usage in the stream so the
		// Anthropic message_delta can carry real token counts.
		// Upstreams that do not understand it ignore it.
		raw["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, "", err
	}
	return raw, body, displayModel, nil
}

// ---- response translation: OpenAI -> Anthropic ----

type openAIChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Role      string `json:"role"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// anthropicStopReason maps an OpenAI finish_reason (plus whether the
// response carries tool calls) to an Anthropic stop_reason.
func anthropicStopReason(finishReason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_use"
	}
	switch finishReason {
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// translateChatResponse converts a successful OpenAI response body
// into the Anthropic shape: JSON for non-streaming, an SSE event
// stream for streaming.
func translateChatResponse(respBody []byte, stream bool, displayModel string, estPromptTokens int) ([]byte, error) {
	if stream {
		return translateOpenAISSEToAnthropic(respBody, displayModel, estPromptTokens), nil
	}
	return translateAnthropicResponse(respBody, displayModel, estPromptTokens)
}

// translateAnthropicResponse converts one non-streaming OpenAI chat
// completion into an Anthropic message object.
func translateAnthropicResponse(respBody []byte, displayModel string, estPromptTokens int) ([]byte, error) {
	var resp openAIChatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("upstream response is not valid OpenAI JSON: %v", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("upstream response has no choices")
	}
	ch := resp.Choices[0]
	content := []interface{}{}
	if strings.TrimSpace(ch.Message.Content) != "" {
		content = append(content, map[string]interface{}{"type": "text", "text": ch.Message.Content})
	}
	for _, tc := range ch.Message.ToolCalls {
		input := json.RawMessage("{}")
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			var v interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &v); err == nil {
				if m, ok := v.(map[string]interface{}); ok {
					if b, err := json.Marshal(m); err == nil {
						input = b
					}
				}
			}
		}
		content = append(content, map[string]interface{}{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Function.Name,
			"input": input,
		})
	}
	inTok, outTok := estPromptTokens, 0
	if resp.Usage != nil {
		if resp.Usage.PromptTokens > 0 {
			inTok = resp.Usage.PromptTokens
		}
		outTok = resp.Usage.CompletionTokens
	}
	out := map[string]interface{}{
		"id":            newAnthropicMessageID(),
		"type":          "message",
		"role":          "assistant",
		"model":         displayModel,
		"content":       content,
		"stop_reason":   anthropicStopReason(ch.FinishReason, len(ch.Message.ToolCalls) > 0),
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  inTok,
			"output_tokens": outTok,
		},
	}
	return json.Marshal(out)
}

func writeAnthropicEvent(sb *strings.Builder, event string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	sb.WriteString("event: " + event + "\n")
	sb.WriteString("data: " + string(b) + "\n\n")
}

// translateOpenAISSEToAnthropic converts a buffered OpenAI SSE body
// into an Anthropic SSE event stream: message_start, one
// content_block_start/delta/stop per text or tool_use block, then
// message_delta (stop_reason + final usage) and message_stop.
// Text and tool blocks are closed before the other kind opens, so
// the event order stays valid even if the upstream interleaves them.
// A usage chunk (when the upstream sends one) replaces the prompt
// estimate in the final usage; without it, message_start carries the
// gateway's estimate and output_tokens stays 0.
func translateOpenAISSEToAnthropic(respBody []byte, displayModel string, estPromptTokens int) []byte {
	var sb strings.Builder
	msgID := newAnthropicMessageID()
	writeAnthropicEvent(&sb, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         displayModel,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":  estPromptTokens,
				"output_tokens": 0,
			},
		},
	})
	nextBlock := 0
	textIdx := -1
	toolIdx := map[int]int{}
	finishReason := ""
	hasToolCalls := false
	inTok, outTok := estPromptTokens, 0
	closeText := func() {
		if textIdx >= 0 {
			writeAnthropicEvent(&sb, "content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": textIdx,
			})
			textIdx = -1
		}
	}
	closeTools := func() {
		for oi, ai := range toolIdx {
			writeAnthropicEvent(&sb, "content_block_stop", map[string]interface{}{
				"type":  "content_block_stop",
				"index": ai,
			})
			delete(toolIdx, oi)
		}
	}
	for _, line := range strings.Split(string(respBody), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				inTok = chunk.Usage.PromptTokens
			}
			outTok = chunk.Usage.CompletionTokens
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
			}
			d := ch.Delta
			if d.Content != "" {
				if textIdx < 0 {
					closeTools()
					writeAnthropicEvent(&sb, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": nextBlock,
						"content_block": map[string]interface{}{
							"type": "text",
							"text": "",
						},
					})
					textIdx = nextBlock
					nextBlock++
				}
				writeAnthropicEvent(&sb, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": textIdx,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": d.Content,
					},
				})
			}
			for _, tc := range d.ToolCalls {
				hasToolCalls = true
				ai, open := toolIdx[tc.Index]
				if !open {
					if tc.ID == "" {
						// Arguments without a preceding id chunk
						// cannot start a tool_use block; drop it
						// rather than emit an id-less block.
						continue
					}
					closeText()
					writeAnthropicEvent(&sb, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": nextBlock,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    tc.ID,
							"name":  tc.Function.Name,
							"input": map[string]interface{}{},
						},
					})
					ai = nextBlock
					toolIdx[tc.Index] = ai
					nextBlock++
				}
				if tc.Function.Arguments != "" {
					writeAnthropicEvent(&sb, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": ai,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": tc.Function.Arguments,
						},
					})
				}
			}
		}
	}
	closeText()
	closeTools()
	writeAnthropicEvent(&sb, "message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   anthropicStopReason(finishReason, hasToolCalls),
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"input_tokens":  inTok,
			"output_tokens": outTok,
		},
	})
	writeAnthropicEvent(&sb, "message_stop", map[string]interface{}{
		"type": "message_stop",
	})
	return []byte(sb.String())
}
