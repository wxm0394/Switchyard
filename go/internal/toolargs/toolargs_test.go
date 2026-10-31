package toolargs

import (
	"encoding/json"
	"strings"
	"testing"
)

func schema(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWholeArgumentsUnwrap(t *testing.T) {
	// Double-encoded whole arguments: "{\"a\":1}" -> {"a":1}
	got := RepairArguments(`"{\"a\": 1}"`, nil)
	if got != `{"a":1}` {
		t.Fatalf("whole unwrap failed: %s", got)
	}
}

func TestWholeArgumentsNonObjectUntouched(t *testing.T) {
	// Inner is an array, not an object: untouched.
	if got := RepairArguments(`"[1,2]"`, nil); got != `"[1,2]"` {
		t.Fatalf("non-object unwrap must be untouched: %s", got)
	}
	// Triple encoding: inner is still a string, not an object: untouched.
	once, _ := json.Marshal(`{"a":1}`)
	twice, _ := json.Marshal(string(once))
	if got := RepairArguments(string(twice), nil); got != string(twice) {
		t.Fatalf("triple encoding must be untouched: %s", got)
	}
}

func TestArrayParamDecoded(t *testing.T) {
	sch := schema(t, `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}`)
	got := RepairArguments(`{"tags": "[\"a\", \"b\"]"}`, sch)
	if !strings.Contains(got, `"tags":["a","b"]`) {
		t.Fatalf("array param not decoded: %s", got)
	}
}

func TestObjectParamDecoded(t *testing.T) {
	sch := schema(t, `{"type":"object","properties":{"filter":{"type":"object","properties":{"city":{"type":"string"}}}}}`)
	got := RepairArguments(`{"filter": "{\"city\": \"Tokyo\"}"}`, sch)
	if !strings.Contains(got, `"city":"Tokyo"`) || strings.Contains(got, `\"city\"`) {
		t.Fatalf("object param not decoded: %s", got)
	}
}

func TestNestedRecursive(t *testing.T) {
	sch := schema(t, `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"array","items":{"type":"string"}}}}}}`)
	got := RepairArguments(`{"outer": "{\"inner\": \"[\\\"x\\\"]\"}"}`, sch)
	if !strings.Contains(got, `"inner":["x"]`) {
		t.Fatalf("nested recursive decode failed: %s", got)
	}
}

func TestStringParamNeverTouched(t *testing.T) {
	sch := schema(t, `{"type":"object","properties":{"note":{"type":"string"}}}`)
	orig := `{"note": "[\"looks\", \"like\", \"json\"]"}`
	if got := RepairArguments(orig, sch); got != orig {
		t.Fatalf("string param must never be touched: %s", got)
	}
}

func TestTypeMismatchUntouched(t *testing.T) {
	// Schema says array, string parses to an object: mismatch, untouched.
	sch := schema(t, `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}`)
	orig := `{"tags": "{\"a\": 1}"}`
	if got := RepairArguments(orig, sch); got != orig {
		t.Fatalf("type mismatch must be untouched: %s", got)
	}
}

func TestUnknownKeyUntouched(t *testing.T) {
	sch := schema(t, `{"type":"object","properties":{"known":{"type":"array","items":{"type":"string"}}}}`)
	orig := `{"mystery": "[1, 2]"}`
	if got := RepairArguments(orig, sch); got != orig {
		t.Fatalf("unknown key must be untouched: %s", got)
	}
}

func TestScalarCoercionNotDone(t *testing.T) {
	// Schema says integer, value is string "5": stays "5".
	sch := schema(t, `{"type":"object","properties":{"count":{"type":"integer"}}}`)
	orig := `{"count": "5"}`
	if got := RepairArguments(orig, sch); got != orig {
		t.Fatalf("scalar coercion must NOT happen: %s", got)
	}
}

func TestInvalidArgsUntouched(t *testing.T) {
	if got := RepairArguments(`not json at all`, nil); got != `not json at all` {
		t.Fatalf("invalid args must be untouched: %s", got)
	}
}

func TestRepairResponse(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"tags\": \"[\\\"a\\\"]\"}"}}]},"finish_reason":"tool_calls"}]}`
	tools := []ToolDef{{
		Name:       "search",
		Parameters: schema(t, `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}`),
	}}
	out, changed, names := RepairResponse([]byte(resp), tools)
	if !changed || len(names) != 1 || names[0] != "search" {
		t.Fatalf("repair response failed: changed=%v names=%v out=%s", changed, names, out)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	msg := parsed["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	call := msg["tool_calls"].([]interface{})[0].(map[string]interface{})["function"].(map[string]interface{})
	if call["arguments"] != `{"tags":["a"]}` {
		t.Fatalf("repair response args not fixed: %v", call["arguments"])
	}
}

func TestRepairResponseUnknownToolUntouched(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"evil","arguments":"{\"x\": 1}"}}]},"finish_reason":"tool_calls"}]}`
	tools := []ToolDef{{Name: "search", Parameters: nil}}
	if _, changed, _ := RepairResponse([]byte(resp), tools); changed {
		t.Fatalf("unknown tool must be untouched")
	}
}

func TestRepairResponseEmptyTools(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	if _, changed, _ := RepairResponse([]byte(resp), nil); changed {
		t.Fatalf("empty tool set must repair nothing")
	}
}

func TestRepairResponseDisabled(t *testing.T) {
	old := Enabled
	Enabled = false
	defer func() { Enabled = old }()
	resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"tags\": \"[\\\"a\\\"]\"}"}}]},"finish_reason":"tool_calls"}]}`
	tools := []ToolDef{{Name: "search", Parameters: schema(t, `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}`)}}
	if _, changed, _ := RepairResponse([]byte(resp), tools); changed {
		t.Fatalf("disabled tool-args must not change response")
	}
}
