package validator

import "testing"

func TestValidate(t *testing.T) {
	ok := `{"choices":[{"message":{"content":"4"},"finish_reason":"stop"}]}`
	if r := Validate([]byte(ok), nil, ""); !r.Valid || r.Code != "pass" {
		t.Fatalf("valid response: %+v", r)
	}
	empty := `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`
	if r := Validate([]byte(empty), nil, ""); r.Code != "fail_empty_content" {
		t.Fatalf("empty content: %+v", r)
	}
	noChoices := `{"choices":[]}`
	if r := Validate([]byte(noChoices), nil, ""); r.Code != "fail_no_choices" {
		t.Fatalf("no choices: %+v", r)
	}
	trunc := `{"choices":[{"message":{"content":"short"},"finish_reason":"length"}]}`
	if r := Validate([]byte(trunc), nil, ""); r.Code != "fail_truncated_length" {
		t.Fatalf("truncated: %+v", r)
	}
	toolOK := `{"choices":[{"message":{"content":"","tool_calls":[{"function":{"name":"get_weather","arguments":"{\"city\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`
	if r := Validate([]byte(toolOK), []string{"get_weather"}, ""); !r.Valid {
		t.Fatalf("tool call ok: %+v", r)
	}
	toolUnknown := `{"choices":[{"message":{"content":"","tool_calls":[{"function":{"name":"nope","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	if r := Validate([]byte(toolUnknown), []string{"get_weather"}, ""); r.Code != "fail_tool_unknown" {
		t.Fatalf("unknown tool: %+v", r)
	}
	jsonBad := `{"choices":[{"message":{"content":"not json"},"finish_reason":"stop"}]}`
	if r := Validate([]byte(jsonBad), nil, "json_object"); r.Code != "fail_invalid_json_content" {
		t.Fatalf("json mode bad: %+v", r)
	}
}
