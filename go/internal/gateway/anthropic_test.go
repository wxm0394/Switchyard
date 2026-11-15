package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsIncludesClaudeNames(t *testing.T) {
	gw, _ := testGateway(t, "http://example.invalid/v1")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	gw.HandleModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("models status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byID := map[string]string{}
	for _, d := range body.Data {
		byID[d.ID] = d.DisplayName
	}
	// The coder alias is listed, and so is its Claude name (Claude
	// Desktop discovery only shows claude-* IDs).
	if _, ok := byID["coder"]; !ok {
		t.Errorf("models must list coder, got %+v", byID)
	}
	if byID["claude-sonnet-4-5"] != "Claude Sonnet 4.5" {
		t.Errorf("models must list claude-sonnet-4-5 with its display name, got %+v", byID)
	}
	// reasoning/fast aliases are absent from this fixture, so their
	// Claude names must be absent too.
	if _, ok := byID["claude-opus-4-5"]; ok {
		t.Errorf("claude-opus-4-5 must not appear without a reasoning alias: %+v", byID)
	}
	if _, ok := byID["claude-haiku-4-5"]; ok {
		t.Errorf("claude-haiku-4-5 must not appear without a fast alias: %+v", byID)
	}
}

// A Claude model name sent to the OpenAI endpoint resolves to its
// pool, so every ID /v1/models lists actually routes somewhere.
func TestChatClaudeModelMapsToAlias(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "test-model" {
			t.Errorf("upstream model = %v, want test-model (coder pool)", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"total_tokens":195}}`))
	}))
	defer up.Close()
	gw, mem := testGateway(t, up.URL+"/v1")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"2+2?"}]}`))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 || logs[0].RequestedModel != "claude-sonnet-4-5-20250929" {
		t.Fatalf("audit must keep the original Claude model name, got %+v", logs)
	}
}

func TestAnthropicCountTokens(t *testing.T) {
	gw, _ := testGateway(t, "http://example.invalid/v1")
	// No key -> 401 Anthropic shape.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello world, this is a token-count test"}]}`))
	rec := httptest.NewRecorder()
	gw.HandleAnthropicCountTokens(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no key status %d, want 401", rec.Code)
	}
	// With key -> 200 with a positive estimate; no upstream is called.
	req = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello world, this is a token-count test"}]}`))
	req.Header.Set("x-api-key", "sk-portal-test")
	rec = httptest.NewRecorder()
	gw.HandleAnthropicCountTokens(rec, req)
	if rec.Code != 200 {
		t.Fatalf("count_tokens status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.InputTokens <= 0 {
		t.Errorf("input_tokens = %d, want > 0", body.InputTokens)
	}
}

func TestAnthropicModelMapping(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5-20250929": "coder",
		"claude-opus-4-1-20250828":   "reasoning",
		"claude-haiku-4-5-20251001":  "fast",
		"claude-sonnet-4-5":          "coder",
		"coder":                      "coder",
		"reasoning":                  "reasoning",
		"fast":                       "fast",
		"act":                        "act",
		"DeepSeek-V4-Flash":          "DeepSeek-V4-Flash",
	}
	for in, want := range cases {
		if got := anthropicModelToAlias(in); got != want {
			t.Errorf("anthropicModelToAlias(%q) = %q, want %q", in, got, want)
		}
	}
}

// A tiny max_tokens is floored to 1024 so reasoning models are
// not starved into empty content (Claude Desktop's model check
// asks for 10; haiku/10 502d live before this floor).
func TestTranslateAnthropicRequestMaxTokensFloor(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{10, 1024},
		{100, 1024},
		{1023, 1024},
		{1024, 1024},
		{5000, 5000},
		{0, 4096},
	} {
		req := &anthropicRequest{
			Model:     "claude-haiku-4-5",
			MaxTokens: tc.in,
			Messages:  []anthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		}
		raw, _, _, err := translateAnthropicRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if raw["max_tokens"] != tc.want {
			t.Errorf("max_tokens %d -> %v, want %d", tc.in, raw["max_tokens"], tc.want)
		}
	}
}

func TestTranslateAnthropicRequest(t *testing.T) {
	rawJSON := `{
		"model": "claude-sonnet-4-5-20250929",
		"max_tokens": 8000,
		"system": [{"type": "text", "text": "You are a coder.", "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "2+2?"}],
		"tools": [{"name": "bash", "description": "Run a command", "input_schema": {"type": "object", "properties": {"cmd": {"type": "string"}}}}],
		"tool_choice": {"type": "any"},
		"temperature": 0.2,
		"stop_sequences": ["STOP"],
		"stream": true
	}`
	var req anthropicRequest
	if err := json.Unmarshal([]byte(rawJSON), &req); err != nil {
		t.Fatal(err)
	}
	raw, body, display, err := translateAnthropicRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	if display != "claude-sonnet-4-5-20250929" {
		t.Errorf("display model = %q", display)
	}
	if raw["model"] != "coder" {
		t.Errorf("model = %v, want coder", raw["model"])
	}
	if raw["max_tokens"] != 8000 {
		t.Errorf("max_tokens = %v", raw["max_tokens"])
	}
	msgs, ok := raw["messages"].([]interface{})
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v", raw["messages"])
	}
	sys := msgs[0].(map[string]interface{})
	if sys["role"] != "system" || sys["content"] != "You are a coder." {
		t.Errorf("system message = %v", sys)
	}
	tools, ok := raw["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", raw["tools"])
	}
	fn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["name"] != "bash" {
		t.Errorf("tool name = %v", fn["name"])
	}
	params := fn["parameters"].(map[string]interface{})
	if params["type"] != "object" {
		t.Errorf("tool params = %v", params)
	}
	if raw["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required", raw["tool_choice"])
	}
	if raw["temperature"] != 0.2 {
		t.Errorf("temperature = %v", raw["temperature"])
	}
	so, ok := raw["stream_options"].(map[string]interface{})
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", raw["stream_options"])
	}
	// Body must be the same JSON the pipeline will forward upstream.
	var reparsed map[string]interface{}
	if err := json.Unmarshal(body, &reparsed); err != nil {
		t.Fatal(err)
	}
	if reparsed["model"] != "coder" {
		t.Errorf("body model = %v", reparsed["model"])
	}
}

func TestTranslateAnthropicMessagesToolRoundTrip(t *testing.T) {
	in := []anthropicMessage{
		{Role: "assistant", Content: json.RawMessage(`[
			{"type": "text", "text": "Running it."},
			{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": {"cmd": "pwd"}},
			{"type": "thinking", "thinking": "should be dropped"}
		]`)},
		{Role: "user", Content: json.RawMessage(`[
			{"type": "tool_result", "tool_use_id": "toolu_1", "content": "/home/root"},
			{"type": "text", "text": "done?"}
		]`)},
	}
	msgs := translateAnthropicMessages(in)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3: %v", len(msgs), msgs)
	}
	asst := msgs[0].(map[string]interface{})
	if asst["content"] != "Running it." {
		t.Errorf("assistant content = %v", asst["content"])
	}
	tcs, ok := asst["tool_calls"].([]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls = %v", asst["tool_calls"])
	}
	tc := tcs[0].(map[string]interface{})
	if tc["id"] != "toolu_1" {
		t.Errorf("tool_call id = %v", tc["id"])
	}
	if tc["function"].(map[string]interface{})["arguments"] != `{"cmd": "pwd"}` {
		t.Errorf("arguments = %v", tc["function"].(map[string]interface{})["arguments"])
	}
	toolMsg := msgs[1].(map[string]interface{})
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "toolu_1" || toolMsg["content"] != "/home/root" {
		t.Errorf("tool message = %v", toolMsg)
	}
	userMsg := msgs[2].(map[string]interface{})
	if userMsg["role"] != "user" {
		t.Errorf("third message = %v", userMsg)
	}
	// Thinking-only assistant message is dropped.
	onlyThinking := []anthropicMessage{
		{Role: "assistant", Content: json.RawMessage(`[{"type": "thinking", "thinking": "hmm"}]`)},
		{Role: "user", Content: json.RawMessage(`"hi"`)},
	}
	msgs = translateAnthropicMessages(onlyThinking)
	if len(msgs) != 1 {
		t.Errorf("thinking-only message must be dropped, got %v", msgs)
	}
}

func TestTranslateAnthropicResponse(t *testing.T) {
	// Text answer.
	out, err := translateAnthropicResponse([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`), "claude-sonnet-4-5", 99)
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" || msg.Model != "claude-sonnet-4-5" || msg.StopReason != "end_turn" {
		t.Errorf("message = %+v", msg)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "4" {
		t.Errorf("content = %+v", msg.Content)
	}
	if msg.Usage.InputTokens != 10 || msg.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want real upstream usage", msg.Usage)
	}
	// Tool calls + length.
	out, err = translateAnthropicResponse([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":50,"completion_tokens":20}}`), "claude-opus-4-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var msg2 struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string                 `json:"type"`
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &msg2); err != nil {
		t.Fatal(err)
	}
	if msg2.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", msg2.StopReason)
	}
	if len(msg2.Content) != 1 || msg2.Content[0].Type != "tool_use" || msg2.Content[0].Input["cmd"] != "pwd" {
		t.Errorf("content = %+v", msg2.Content)
	}
	// length -> max_tokens.
	out, err = translateAnthropicResponse([]byte(`{"choices":[{"message":{"content":"abc"},"finish_reason":"length"}]}`), "claude-sonnet-4-5", 42)
	if err != nil {
		t.Fatal(err)
	}
	var msg3 struct {
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &msg3); err != nil {
		t.Fatal(err)
	}
	if msg3.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens", msg3.StopReason)
	}
	if msg3.Usage.InputTokens != 42 {
		t.Errorf("usage fallback must use the prompt estimate, got %+v", msg3.Usage)
	}
}

func TestTranslateOpenAISSE(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n" +
		"data: [DONE]\n"
	out := string(translateOpenAISSEToAnthropic([]byte(sse), "claude-sonnet-4-5", 99))
	for _, want := range []string{
		"event: message_start",
		"\"type\":\"message_start\"",
		"event: content_block_start",
		"\"type\":\"text_delta\"",
		"\"text\":\"Hel\"",
		"\"text\":\"lo\"",
		"event: content_block_stop",
		"event: message_delta",
		"\"stop_reason\":\"end_turn\"",
		"\"input_tokens\":10",
		"\"output_tokens\":2",
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SSE output missing %q:\n%s", want, out)
		}
	}
	// message_start must carry the estimate (usage chunk arrives last).
	if !strings.Contains(out, "\"input_tokens\":99") {
		t.Errorf("message_start must carry the prompt estimate 99:\n%s", out)
	}
}

func TestTranslateOpenAISSEToolCall(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"function\":{\"name\":\"bash\",\"arguments\":\"\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"cmd\\\"\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":\\\"pwd\\\"}\"}}]}}]}\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n" +
		"data: [DONE]\n"
	out := string(translateOpenAISSEToAnthropic([]byte(sse), "claude-sonnet-4-5", 50))
	for _, want := range []string{
		"\"type\":\"tool_use\"",
		"\"id\":\"call_9\"",
		"\"name\":\"bash\"",
		"\"type\":\"input_json_delta\"",
		"\"partial_json\":\"{\\\"cmd\\\"\"",
		"\"stop_reason\":\"tool_use\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SSE output missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\"type\":\"content_block_start\"") != 1 {
		t.Errorf("want exactly 1 tool_use block start:\n%s", out)
	}
}

func TestAnthropicHandlerEndToEnd(t *testing.T) {
	var lastUpstream map[string]interface{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastUpstream)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"4"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer up.Close()
	gw, mem := testGateway(t, up.URL+"/v1")

	// No key -> 401 Anthropic shape.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"2+2?"}]}`))
	rec := httptest.NewRecorder()
	gw.HandleAnthropicMessages(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no key status %d, want 401", rec.Code)
	}
	var errBody struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody.Type != "error" || errBody.Error.Type != "authentication_error" {
		t.Fatalf("401 body must be Anthropic error shape: %s", rec.Body.String())
	}

	// With x-api-key -> 200 Anthropic message; upstream got the
	// translated OpenAI request (model substituted, system kept).
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5-20250929","max_tokens":100,"system":"Be terse.","messages":[{"role":"user","content":"2+2?"}]}`))
	req.Header.Set("x-api-key", "sk-portal-test")
	rec = httptest.NewRecorder()
	gw.HandleAnthropicMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("messages status %d body %s", rec.Code, rec.Body.String())
	}
	var msg struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" || msg.Model != "claude-sonnet-4-5-20250929" || msg.StopReason != "end_turn" {
		t.Errorf("message = %+v", msg)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "4" {
		t.Errorf("content = %+v", msg.Content)
	}
	if lastUpstream["model"] != "test-model" {
		t.Errorf("upstream model = %v, want test-model", lastUpstream["model"])
	}
	upMsgs, _ := lastUpstream["messages"].([]interface{})
	if len(upMsgs) != 2 || upMsgs[0].(map[string]interface{})["role"] != "system" {
		t.Errorf("upstream messages = %v, want system + user", lastUpstream["messages"])
	}
	logs, _ := mem.ListAudit(10, 0)
	if len(logs) != 1 || logs[0].RequestedModel != "coder" || logs[0].StatusCode != 200 {
		t.Fatalf("audit = %+v, want one 200 row requesting coder", logs)
	}

	// Tool round-trip: upstream returns a tool call, the client
	// answers with a tool_result, the upstream sees a role=tool
	// message on the second turn.
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastUpstream)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":10}}`))
	}))
	defer up2.Close()
	gw2, _ := testGateway(t, up2.URL+"/v1")
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"run pwd"}]}`))
	req.Header.Set("x-api-key", "sk-portal-test")
	rec = httptest.NewRecorder()
	gw2.HandleAnthropicMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("tool call status %d body %s", rec.Code, rec.Body.String())
	}
	var msg2 struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string                 `json:"type"`
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg2); err != nil {
		t.Fatal(err)
	}
	if msg2.StopReason != "tool_use" || len(msg2.Content) != 1 || msg2.Content[0].Input["cmd"] != "pwd" {
		t.Fatalf("tool_use response = %+v", msg2)
	}
	// Second turn with the tool result.
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[
		{"role":"user","content":"run pwd"},
		{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"bash","input":{"cmd":"pwd"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"/home/root"}]}
	]}`))
	req.Header.Set("x-api-key", "sk-portal-test")
	rec = httptest.NewRecorder()
	gw2.HandleAnthropicMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("tool result status %d body %s", rec.Code, rec.Body.String())
	}
	upMsgs, _ = lastUpstream["messages"].([]interface{})
	if len(upMsgs) != 3 {
		t.Fatalf("upstream messages = %v, want user + assistant(tool_calls) + tool", lastUpstream["messages"])
	}
	toolMsg := upMsgs[2].(map[string]interface{})
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" || toolMsg["content"] != "/home/root" {
		t.Errorf("upstream tool message = %v", toolMsg)
	}
}

func TestAnthropicHandlerStreaming(t *testing.T) {
	var lastUpstream map[string]interface{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastUpstream)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n" +
			"data: [DONE]\n"))
	}))
	defer up.Close()
	gw, _ := testGateway(t, up.URL+"/v1")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleAnthropicMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("stream status %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: message_start", "\"type\":\"text_delta\"", "\"text\":\"Hel\"", "event: message_stop", "\"stop_reason\":\"end_turn\""} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q:\n%s", want, body)
		}
	}
	if so, ok := lastUpstream["stream_options"].(map[string]interface{}); !ok || so["include_usage"] != true {
		t.Errorf("upstream stream_options = %v, want include_usage true", lastUpstream["stream_options"])
	}
}
