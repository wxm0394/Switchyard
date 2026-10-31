package toolrescue

import (
	"encoding/json"
	"strings"
	"testing"
)

func names(calls []ToolCall) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.Name)
	}
	return out
}

func TestTokenDialect(t *testing.T) {
	content := `<|tool_calls_section_begin|><|tool_call_begin|>functions.get_weather:0<|tool_call_argument_begin|>{"city": "Tokyo"}<|tool_call_end|><|tool_calls_section_end|>`
	calls, ok := Rescue(content, []string{"get_weather"})
	if !ok || len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("token dialect failed: ok=%v calls=%v", ok, calls)
	}
	if !strings.Contains(calls[0].Arguments, "Tokyo") {
		t.Fatalf("token args lost: %s", calls[0].Arguments)
	}
}

func TestFunctionTagDialect(t *testing.T) {
	content := `<function=get_weather>{"city": "Osaka"}</function>`
	calls, ok := Rescue(content, []string{"get_weather"})
	if !ok || len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("function-tag dialect failed: ok=%v calls=%v", ok, calls)
	}
}

func TestFunctionTagNoArgs(t *testing.T) {
	content := `<function=get_time></function>`
	calls, ok := Rescue(content, []string{"get_time"})
	if !ok || calls[0].Arguments != "{}" {
		t.Fatalf("no-arg function tag failed: ok=%v calls=%v", ok, calls)
	}
}

func TestXMLDialect(t *testing.T) {
	content := `<tool_call>{"name": "get_weather", "arguments": {"city": "Kyoto"}}</tool_call>`
	calls, ok := Rescue(content, []string{"get_weather"})
	if !ok || len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("xml dialect failed: ok=%v calls=%v", ok, calls)
	}
}

func TestXMLDialectArray(t *testing.T) {
	content := `<tool_call>[{"name": "a", "arguments": {}}, {"name": "b", "arguments": {"x": 1}}]</tool_call>`
	calls, ok := Rescue(content, []string{"a", "b"})
	if !ok || len(calls) != 2 {
		t.Fatalf("xml array failed: ok=%v calls=%v", ok, calls)
	}
}

func TestBareJSON(t *testing.T) {
	content := `{"name": "get_weather", "arguments": "{\"city\": \"Nara\"}"}`
	calls, ok := Rescue(content, []string{"get_weather"})
	if !ok || len(calls) != 1 {
		t.Fatalf("bare json failed: ok=%v calls=%v", ok, calls)
	}
}

func TestFencedJSON(t *testing.T) {
	content := "```json\n{\"name\": \"get_weather\", \"arguments\": {}}\n```"
	calls, ok := Rescue(content, []string{"get_weather"})
	if !ok || len(calls) != 1 {
		t.Fatalf("fenced json failed: ok=%v calls=%v", ok, calls)
	}
}

func TestProsePlusBlockNotRescued(t *testing.T) {
	// v1 conservative rule: prose plus a block is left alone.
	content := "Here is an example:\n```json\n{\"name\": \"get_weather\", \"arguments\": {}}\n```\nDo not run it."
	if _, ok := Rescue(content, []string{"get_weather"}); ok {
		t.Fatalf("prose+block must not rescue")
	}
}

func TestUnknownToolNotRescued(t *testing.T) {
	content := `<function=evil_tool>{"x": 1}</function>`
	if _, ok := Rescue(content, []string{"get_weather"}); ok {
		t.Fatalf("unknown tool must not rescue")
	}
}

func TestMajorityGate(t *testing.T) {
	// Marker block is a small fraction of a long explanation: must not rescue.
	long := strings.Repeat("explanation sentence. ", 100)
	content := long + `<function=get_weather>{"city": "Tokyo"}</function>`
	if _, ok := Rescue(content, []string{"get_weather"}); ok {
		t.Fatalf("majority gate must block small embedded block")
	}
}

func TestAllBlocksMustPass(t *testing.T) {
	// One good block + one unknown block: whole rescue fails open.
	content := `<function=get_weather>{"city": "Tokyo"}</function><function=evil>{"x": 1}</function>`
	if _, ok := Rescue(content, []string{"get_weather"}); ok {
		t.Fatalf("mixed good+unknown blocks must fail open")
	}
}

func TestNoToolsNoRescue(t *testing.T) {
	if _, ok := Rescue(`{"name": "x", "arguments": {}}`, nil); ok {
		t.Fatalf("no declared tools must not rescue")
	}
}

func TestRescueResponseKeepsContent(t *testing.T) {
	resp := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"<function=get_weather>{\"city\": \"Tokyo\"}</function>"},"finish_reason":"stop"}]}`
	out, changed, _ := RescueResponse([]byte(resp), []string{"get_weather"})
	if !changed {
		t.Fatalf("expected rescue to change response")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	msg := parsed["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	if msg["content"] != `<function=get_weather>{"city": "Tokyo"}</function>` {
		t.Fatalf("original content must be kept, got %v", msg["content"])
	}
	if parsed["choices"].([]interface{})[0].(map[string]interface{})["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason must become tool_calls")
	}
	tc := msg["tool_calls"].([]interface{})
	if len(tc) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tc))
	}
	if id := tc[0].(map[string]interface{})["id"].(string); !strings.HasPrefix(id, "call_") || len(id) != 21 {
		t.Fatalf("bad call id: %q", id)
	}
}

func TestRescueResponseHappyPathUntouched(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"hi","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	out, changed, _ := RescueResponse([]byte(resp), []string{"get_weather"})
	if changed || string(out) != resp {
		t.Fatalf("happy path must be untouched")
	}
}

func TestRescueResponseDisabled(t *testing.T) {
	old := Enabled
	Enabled = false
	defer func() { Enabled = old }()
	resp := `{"choices":[{"message":{"role":"assistant","content":"<function=get_weather>{}</function>"},"finish_reason":"stop"}]}`
	if _, changed, _ := RescueResponse([]byte(resp), []string{"get_weather"}); changed {
		t.Fatalf("disabled rescue must not change response")
	}
}
