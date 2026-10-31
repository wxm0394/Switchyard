// Package toolrescue ports server/tool-rescue.ts (Railways v1) exactly.
//
// Scope: non-streaming responses only. Streaming responses are passed
// through untouched, as in Node. The function never throws/panics on the
// hot path: any doubt returns the input unchanged.
//
// Two deliberate conservative rules from Node, preserved here:
//  1. Detected-but-unparseable FAILS OPEN: the original response is
//     returned unchanged instead of failing over. Only a fully parseable,
//     schema-gated rescue changes the response.
//  2. The original content is KEPT: only tool_calls and finish_reason
//     are added, nothing is deleted.
//
// Kill switch: set Enabled = false and redeploy. This mirrors Node's
// TOOL_CALL_RESCUE_ENABLED constant (not an environment variable).
package toolrescue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strings"
)

// Enabled is the kill switch. Default true, matching Node.
var Enabled = true

// ToolCall is one rescued call, in OpenAI function.arguments shape:
// Arguments is a JSON string of a JSON object.
type ToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// majorityThreshold is the Node MAJORITY_THRESHOLD (0.5): for marker
// dialects, the dialect blocks must account for at least half of the
// content, otherwise a long explanation with one embedded block is an
// example, not a call, and must not execute. Bare/fenced JSON is
// whole-content by construction and needs no gate.
const majorityThreshold = 0.5

func extractBalancedJSON(text string, from int) (string, int, bool) {
	if from < 0 || from >= len(text) {
		return "", 0, false
	}
	open := text[from]
	if open != '{' && open != '[' {
		return "", 0, false
	}
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth := 0
	inString := false
	escaped := false
	for i := from; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
		} else if ch == open {
			depth++
		} else if ch == closeCh {
			depth--
			if depth == 0 {
				return text[from : i+1], i + 1, true
			}
		}
	}
	return "", 0, false
}

func callFromNamedJSON(jsonStr string, toolNames map[string]bool) (ToolCall, bool) {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return ToolCall{}, false
	}
	if obj == nil {
		return ToolCall{}, false
	}
	name, _ := obj["name"].(string)
	if name == "" || !toolNames[name] {
		return ToolCall{}, false
	}
	rawArgs, ok := obj["arguments"]
	if !ok {
		rawArgs, ok = obj["parameters"]
		if !ok {
			rawArgs = map[string]interface{}{}
		}
	}
	switch a := rawArgs.(type) {
	case string:
		var parsed interface{}
		if err := json.Unmarshal([]byte(a), &parsed); err != nil {
			return ToolCall{}, false
		}
		if parsed == nil {
			return ToolCall{}, false
		}
		if _, isObj := parsed.(map[string]interface{}); !isObj {
			return ToolCall{}, false
		}
		return ToolCall{Name: name, Arguments: a}, true
	case map[string]interface{}:
		b, err := json.Marshal(a)
		if err != nil {
			return ToolCall{}, false
		}
		return ToolCall{Name: name, Arguments: string(b)}, true
	default:
		return ToolCall{}, false
	}
}

func callsFromJSONValue(jsonStr string, toolNames map[string]bool) ([]ToolCall, bool) {
	var raw interface{}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, false
	}
	if arr, ok := raw.([]interface{}); ok {
		if len(arr) == 0 {
			return nil, false
		}
		calls := []ToolCall{}
		for _, el := range arr {
			b, err := json.Marshal(el)
			if err != nil {
				return nil, false
			}
			c, ok := callFromNamedJSON(string(b), toolNames)
			if !ok {
				return nil, false
			}
			calls = append(calls, c)
		}
		return calls, true
	}
	c, ok := callFromNamedJSON(jsonStr, toolNames)
	if !ok {
		return nil, false
	}
	return []ToolCall{c}, true
}

type parsedDialect struct {
	calls   []ToolCall
	covered int
}

var tokenCallRe = regexp.MustCompile(`<\|tool_call_begin\|>\s*([\s\S]*?)\s*<\|tool_call_argument_begin\|>\s*`)
var tokenNameRe = regexp.MustCompile(`^functions\.([A-Za-z0-9_.\-]+):\d+$`)
var functionNameRe = regexp.MustCompile(`^<function=([A-Za-z0-9_.\-]+)`)

func parseTokenDialect(text string, toolNames map[string]bool) (*parsedDialect, bool) {
	clean := strings.ReplaceAll(text, "<|tool_calls_section_begin|>", "")
	clean = strings.ReplaceAll(clean, "<|tool_calls_section_end|>", "")
	locs := tokenCallRe.FindAllStringSubmatchIndex(clean, -1)
	if len(locs) == 0 {
		return nil, false
	}
	calls := []ToolCall{}
	covered := 0
	for _, loc := range locs {
		// loc: full match [start,end], group1 [start,end]
		idToken := strings.TrimSpace(clean[loc[2]:loc[3]])
		argStart := loc[1]
		jsonStart := strings.IndexByte(clean[argStart:], '{')
		if jsonStart < 0 {
			return nil, false
		}
		jsonStart += argStart
		extracted, end, ok := extractBalancedJSON(clean, jsonStart)
		if !ok {
			return nil, false
		}
		m := tokenNameRe.FindStringSubmatch(idToken)
		if m == nil {
			return nil, false
		}
		name := m[1]
		if !toolNames[name] {
			return nil, false
		}
		var argsObj interface{}
		if err := json.Unmarshal([]byte(extracted), &argsObj); err != nil {
			return nil, false
		}
		if _, isObj := argsObj.(map[string]interface{}); !isObj {
			return nil, false
		}
		calls = append(calls, ToolCall{Name: name, Arguments: extracted})
		if tail := clean[end:]; strings.HasPrefix(tail, "<|tool_call_end|>") {
			// Node matches /^\s*<\|tool_call_end\|>/ on the tail after the
			// JSON; the tail here starts right after the JSON, and any
			// whitespace before the end marker is part of that match.
			// Handle optional leading whitespace explicitly.
			end += len("<|tool_call_end|>")
		} else if m2 := regexp.MustCompile(`^\s*<\|tool_call_end\|>`).FindStringIndex(tail); m2 != nil {
			end += m2[1]
		}
		covered += end - loc[0]
	}
	if len(calls) == 0 {
		return nil, false
	}
	return &parsedDialect{calls: calls, covered: covered}, true
}

func parseFunctionTags(text string, toolNames map[string]bool) (*parsedDialect, bool) {
	calls := []ToolCall{}
	covered := 0
	cursor := 0
	found := false
	for {
		start := strings.Index(text[cursor:], "<function=")
		if start < 0 {
			break
		}
		start += cursor
		found = true
		m := functionNameRe.FindStringSubmatch(text[start:])
		if m == nil {
			return nil, false
		}
		name := m[1]
		if !toolNames[name] {
			return nil, false
		}
		afterName := start + len(m[0])
		closeIdx := strings.Index(text[afterName:], "</function>")
		if closeIdx < 0 {
			return nil, false
		}
		closeIdx += afterName
		region := text[afterName:closeIdx]
		jsonStartRel := strings.IndexByte(region, '{')
		var args string
		if jsonStartRel < 0 {
			if strings.TrimSpace(region) != "" && strings.TrimSpace(region) != ">" {
				return nil, false
			}
			args = "{}"
		} else {
			extracted, _, ok := extractBalancedJSON(region, jsonStartRel)
			if !ok {
				return nil, false
			}
			var argsObj interface{}
			if err := json.Unmarshal([]byte(extracted), &argsObj); err != nil {
				return nil, false
			}
			if _, isObj := argsObj.(map[string]interface{}); !isObj {
				return nil, false
			}
			args = extracted
		}
		calls = append(calls, ToolCall{Name: name, Arguments: args})
		covered += closeIdx + len("</function>") - start
		cursor = closeIdx + len("</function>")
	}
	if !found || len(calls) == 0 {
		return nil, false
	}
	return &parsedDialect{calls: calls, covered: covered}, true
}

func parseToolCallXML(text string, toolNames map[string]bool) (*parsedDialect, bool) {
	calls := []ToolCall{}
	covered := 0
	cursor := 0
	found := false
	for {
		start := strings.Index(text[cursor:], "<tool_call>")
		if start < 0 {
			break
		}
		start += cursor
		found = true
		closeIdx := strings.Index(text[start:], "</tool_call>")
		if closeIdx < 0 {
			return nil, false
		}
		closeIdx += start
		region := text[start+len("<tool_call>") : closeIdx]
		brace := strings.IndexByte(region, '{')
		bracket := strings.IndexByte(region, '[')
		first := -1
		if brace >= 0 && bracket >= 0 {
			if brace < bracket {
				first = brace
			} else {
				first = bracket
			}
		} else if brace >= 0 {
			first = brace
		} else {
			first = bracket
		}
		if first < 0 {
			return nil, false
		}
		extracted, _, ok := extractBalancedJSON(region, first)
		if !ok {
			return nil, false
		}
		parsed, ok := callsFromJSONValue(extracted, toolNames)
		if !ok {
			return nil, false
		}
		calls = append(calls, parsed...)
		covered += closeIdx + len("</tool_call>") - start
		cursor = closeIdx + len("</tool_call>")
	}
	if !found || len(calls) == 0 {
		return nil, false
	}
	return &parsedDialect{calls: calls, covered: covered}, true
}

var fenceRe = regexp.MustCompile("(?s)^```(?:json)?\\s*\\n?([\\s\\S]*?)\\n?```\\s*$")

func parseBareOrFencedJSON(text string, toolNames map[string]bool) ([]ToolCall, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "```") {
		m := fenceRe.FindStringSubmatch(trimmed)
		if m == nil {
			return nil, false
		}
		return callsFromJSONValue(strings.TrimSpace(m[1]), toolNames)
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return callsFromJSONValue(trimmed, toolNames)
	}
	return nil, false
}

// Rescue parses plain-text tool calls from content. It returns
// (calls, true) only when a dialect is detected AND every detected block
// parses to a declared tool AND the majority gate passes for marker
// dialects. Any doubt returns (nil, false); callers must then return the
// response unchanged.
func Rescue(content string, toolNamesList []string) ([]ToolCall, bool) {
	if !Enabled {
		return nil, false
	}
	toolNames := map[string]bool{}
	for _, n := range toolNamesList {
		if n != "" {
			toolNames[n] = true
		}
	}
	if len(toolNames) == 0 {
		return nil, false
	}
	var parsed *parsedDialect
	gated := false
	switch {
	case strings.Contains(content, "<|tool_call_begin|>") || strings.Contains(content, "<|tool_calls_section_begin|>"):
		p, ok := parseTokenDialect(content, toolNames)
		if !ok {
			return nil, false
		}
		parsed, gated = p, true
	case strings.Contains(content, "<function="):
		p, ok := parseFunctionTags(content, toolNames)
		if !ok {
			return nil, false
		}
		parsed, gated = p, true
	case strings.Contains(content, "<tool_call>"):
		p, ok := parseToolCallXML(content, toolNames)
		if !ok {
			return nil, false
		}
		parsed, gated = p, true
	default:
		bare, ok := parseBareOrFencedJSON(content, toolNames)
		if !ok {
			return nil, false
		}
		parsed = &parsedDialect{calls: bare, covered: len(content)}
	}
	if parsed == nil || len(parsed.calls) == 0 {
		return nil, false
	}
	if gated && len(content) > 0 && float64(parsed.covered)/float64(len(content)) < majorityThreshold {
		return nil, false
	}
	return parsed.calls, true
}

func newCallID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "call_" + hex.EncodeToString(b)
}

// RescueResponse applies Rescue to a non-streaming OpenAI-shaped response
// body. It returns (newBody, changed, rescuedNames). On any doubt it
// returns the original body unchanged. The original content is kept;
// only tool_calls and finish_reason are added. Streaming bodies must
// never be passed here.
func RescueResponse(respBody []byte, toolNamesList []string) (out []byte, changed bool, names []string) {
	if !Enabled || len(toolNamesList) == 0 {
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
	choice, _ := choices[0].(map[string]interface{})
	if choice == nil {
		return respBody, false, nil
	}
	msg, _ := choice["message"].(map[string]interface{})
	if msg == nil {
		return respBody, false, nil
	}
	content, _ := msg["content"].(string)
	if content == "" {
		return respBody, false, nil
	}
	if tc, ok := msg["tool_calls"].([]interface{}); ok && len(tc) > 0 {
		return respBody, false, nil
	}
	calls, ok := Rescue(content, toolNamesList)
	if !ok || len(calls) == 0 {
		return respBody, false, nil
	}
	toolCalls := []interface{}{}
	for _, c := range calls {
		id := newCallID()
		if id == "" {
			return respBody, false, nil
		}
		toolCalls = append(toolCalls, map[string]interface{}{
			"id":   id,
			"type": "function",
			"function": map[string]interface{}{
				"name":      c.Name,
				"arguments": c.Arguments,
			},
		})
		names = append(names, c.Name)
	}
	msg["tool_calls"] = toolCalls
	choice["finish_reason"] = "tool_calls"
	out, err := json.Marshal(resp)
	if err != nil {
		return respBody, false, nil
	}
	log.Printf("[TOOL-RESCUE] rescued %d tool call(s): %s", len(names), strings.Join(names, ","))
	return out, true, names
}
