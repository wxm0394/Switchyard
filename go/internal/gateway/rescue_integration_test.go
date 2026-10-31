package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// End-to-end through HandleChat (non-streaming): upstream returns a
// plain-text function-tag call, the gateway rescues it into tool_calls
// before validation, and the client sees finish_reason tool_calls with
// the original content kept.
func TestChatRescueIntegration(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-r","choices":[{"message":{"role":"assistant","content":"<function=get_weather>{\"city\": \"Tokyo\"}</function>"},"finish_reason":"stop"}],"usage":{"total_tokens":50}}`))
	}))
	defer up.Close()
	gw, _ := testGateway(t, up.URL+"/v1")
	body := `{"model":"coder","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	choice := parsed["choices"].([]interface{})[0].(map[string]interface{})
	msg := choice["message"].(map[string]interface{})
	if msg["content"] != `<function=get_weather>{"city": "Tokyo"}</function>` {
		t.Fatalf("original content must be kept: %v", msg["content"])
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", choice["finish_reason"])
	}
	tc, ok := msg["tool_calls"].([]interface{})
	if !ok || len(tc) != 1 {
		t.Fatalf("expected 1 rescued tool_call, got %v", msg["tool_calls"])
	}
	fn := tc[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Fatalf("rescued tool name = %v", fn["name"])
	}
}

// End-to-end through HandleChat (non-streaming): upstream returns a
// double-encoded array argument, the gateway repairs it before
// validation, and the client sees the decoded array.
func TestChatToolArgsIntegration(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-a","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"tags\": \"[\\\"a\\\"]\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":50}}`))
	}))
	defer up.Close()
	gw, _ := testGateway(t, up.URL+"/v1")
	body := `{"model":"coder","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	msg := parsed["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	fn := msg["tool_calls"].([]interface{})[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["arguments"] != `{"tags":["a"]}` {
		t.Fatalf("arguments not repaired: %v", fn["arguments"])
	}
}

// Streaming responses must pass through untouched: no rescue, no args
// repair, even when the streamed body contains a function tag.
func TestChatStreamingUntouched(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-s","choices":[{"message":{"role":"assistant","content":"<function=get_weather>{}</function>"},"finish_reason":"stop"}],"usage":{"total_tokens":50}}`))
	}))
	defer up.Close()
	gw, _ := testGateway(t, up.URL+"/v1")
	body := `{"model":"coder","stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{}}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-portal-test")
	rec := httptest.NewRecorder()
	gw.HandleChat(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tool_calls") {
		t.Fatalf("streaming response must not be rescued: %s", rec.Body.String())
	}
}
