// Package toolargs ports server/tool-args.ts (Railways, itself a port of
// freellmapi's tool-args) exactly.
//
// Gate, which is the whole design: the schema must say a parameter is
// an array or object AND the string value must parse to EXACTLY that
// JSON type. A parameter whose schema says "string" is never touched,
// even if it looks like JSON. A type-less schema node is never guessed
// at, and a mismatch is left as-is rather than coerced.
//
// This package does NOT do scalar type coercion (string "5" for an
// integer stays "5"): that is a different, value-changing transform,
// deliberately out of scope in Node and here. Do not add it.
//
// Fail-open, like tool-rescue: any doubt returns the input unchanged.
// Only non-streaming responses pass through; streaming tool deltas are
// out of scope.
//
// Kill switch: set Enabled = false and redeploy. This mirrors Node's
// TOOL_ARGS_ENABLED constant (not an environment variable).
package toolargs

import (
	"encoding/json"
	"log"
	"strings"
)

// Enabled is the kill switch. Default true, matching Node.
var Enabled = true

// ToolDef describes one declared tool for the response wrapper.
// Parameters is the tool's JSON Schema object, or nil when the tool
// declares no parameters (it is still "known" for gating, and still
// gets the schema-free whole-arguments unwrap).
type ToolDef struct {
	Name       string
	Parameters map[string]interface{}
}

func decodeIfSchemaSays(value string, schema map[string]interface{}) (interface{}, bool) {
	if schema == nil {
		return nil, false
	}
	want, _ := schema["type"].(string)
	if want != "array" && want != "object" {
		return nil, false
	}
	trimmed := strings.TrimSpace(value)
	if !(strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{")) {
		return nil, false
	}
	var inner interface{}
	if err := json.Unmarshal([]byte(trimmed), &inner); err != nil {
		return nil, false
	}
	if want == "array" {
		if _, ok := inner.([]interface{}); ok {
			return inner, true
		}
		return nil, false
	}
	if inner != nil {
		if _, ok := inner.(map[string]interface{}); ok {
			return inner, true
		}
	}
	return nil, false
}

func repairInPlace(node interface{}, schema map[string]interface{}, depth int) bool {
	if node == nil || depth > 50 {
		return false
	}
	switch n := node.(type) {
	case []interface{}:
		// One items schema describes every element; a tuple form
		// (items as an array) is deliberately not followed.
		if schema == nil {
			return false
		}
		itemsRaw, ok := schema["items"]
		if !ok {
			return false
		}
		itemSchema, ok := itemsRaw.(map[string]interface{})
		if !ok {
			return false
		}
		changed := false
		for i, value := range n {
			if s, isStr := value.(string); isStr {
				if decoded, ok := decodeIfSchemaSays(s, itemSchema); ok {
					n[i] = decoded
					changed = true
					repairInPlace(decoded, itemSchema, depth+1)
				}
			} else if repairInPlace(value, itemSchema, depth+1) {
				changed = true
			}
		}
		return changed
	case map[string]interface{}:
		if schema == nil {
			return false
		}
		propsRaw, ok := schema["properties"]
		if !ok {
			return false
		}
		props, ok := propsRaw.(map[string]interface{})
		if !ok {
			return false
		}
		changed := false
		for key, value := range n {
			childRaw, ok := props[key]
			if !ok {
				continue // Unknown key: no schema to justify a change.
			}
			child, ok := childRaw.(map[string]interface{})
			if !ok {
				continue
			}
			if s, isStr := value.(string); isStr {
				if decoded, ok := decodeIfSchemaSays(s, child); ok {
					n[key] = decoded
					changed = true
					repairInPlace(decoded, child, depth+1)
				}
			} else if repairInPlace(value, child, depth+1) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

// RepairArguments repairs one tool call's arguments JSON string against
// the tool's parameter schema. It returns the original string unchanged
// whenever anything doesn't parse or doesn't match. It never panics
// for malformed input.
//
// Note on key order: Go's json.Marshal sorts map keys alphabetically,
// while Node's JSON.stringify preserves insertion order. A repaired
// arguments string may therefore list keys in a different order than
// Node's output; the JSON value is identical, and clients parse JSON,
// so this is not a semantic change.
func RepairArguments(args string, paramSchema map[string]interface{}) string {
	var parsed interface{}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return args
	}
	changed := false
	// Whole-arguments double encoding: "{...}" parses to a string that
	// is itself JSON of an object. Unwrap one level; this needs no schema.
	if s, ok := parsed.(string); ok {
		var inner interface{}
		if err := json.Unmarshal([]byte(s), &inner); err != nil {
			return args
		}
		if inner == nil {
			return args
		}
		if _, isObj := inner.(map[string]interface{}); !isObj {
			return args
		}
		parsed = inner
		changed = true
	}
	if parsed == nil {
		return args
	}
	if _, isObj := parsed.(map[string]interface{}); !isObj {
		if changed {
			if b, err := json.Marshal(parsed); err == nil {
				return string(b)
			}
		}
		return args
	}
	if repairInPlace(parsed, paramSchema, 0) {
		changed = true
	}
	if !changed {
		return args
	}
	if b, err := json.Marshal(parsed); err == nil {
		return string(b)
	}
	return args
}

// RepairResponse walks every choice's message.tool_calls in a
// non-streaming OpenAI-shaped response body, repairs each declared
// tool's arguments, and leaves everything else (including finish_reason
// and content) untouched. It returns (newBody, changed, repairedNames).
// On any doubt it returns the original body unchanged. Empty tool set
// repairs nothing, same gate as rescue.
func RepairResponse(respBody []byte, tools []ToolDef) (out []byte, changed bool, repairedNames []string) {
	if !Enabled {
		return respBody, false, nil
	}
	if len(tools) == 0 {
		return respBody, false, nil
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return respBody, false, nil
	}
	choices, _ := resp["choices"].([]interface{})
	if len(choices) == 0 {
		return respBody, false, nil
	}
	declared := map[string]bool{}
	schemas := map[string]map[string]interface{}{}
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		declared[t.Name] = true
		if t.Parameters != nil {
			schemas[t.Name] = t.Parameters
		}
	}
	anyChanged := false
	for _, ch := range choices {
		choice, _ := ch.(map[string]interface{})
		if choice == nil {
			continue
		}
		msg, _ := choice["message"].(map[string]interface{})
		if msg == nil {
			continue
		}
		toolCalls, _ := msg["tool_calls"].([]interface{})
		for _, tc := range toolCalls {
			call, _ := tc.(map[string]interface{})
			if call == nil {
				continue
			}
			fn, _ := call["function"].(map[string]interface{})
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			if name == "" || !declared[name] {
				continue // Unknown tool: leave alone.
			}
			// Arguments must be a string in OpenAI shape; if it is not,
			// leave the call alone rather than coercing it.
			if _, hasArgs := fn["arguments"]; !hasArgs {
				continue
			}
			repaired := RepairArguments(args, schemas[name])
			if repaired != args {
				fn["arguments"] = repaired
				repairedNames = append(repairedNames, name)
				anyChanged = true
			}
		}
	}
	if !anyChanged {
		return respBody, false, nil
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return respBody, false, nil
	}
	log.Printf("[TOOL-ARGS] repaired %d tool call(s): %s", len(repairedNames), strings.Join(repairedNames, ","))
	return out, true, repairedNames
}
