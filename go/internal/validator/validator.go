// Package validator ports server/validator.ts validateResponse.
package validator

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Result struct {
	Valid  bool
	Code   string
	Reason string
}

var refusalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rate\s*limit\s*reached`),
	regexp.MustCompile(`(?i)model\s*is\s*(currently\s*)?overloaded`),
	regexp.MustCompile(`(?i)upstream\s*(connect\s*)?timeout`),
	regexp.MustCompile(`(?i)502\s*bad\s*gateway`),
	regexp.MustCompile(`(?i)504\s*gateway\s*time-?out`),
	regexp.MustCompile(`(?i)<!doctype\s*html>`),
	regexp.MustCompile(`(?i)<html.*?>`),
	regexp.MustCompile(`"error":\s*\{\s*"code"`),
}

type ToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Choice struct {
	Message struct {
		Content   string     `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type Response struct {
	Choices []Choice `json:"choices"`
}

// Validate mirrors Node's check order exactly.
func Validate(body []byte, declaredTools []string, responseFormatType string) Result {
	if len(body) == 0 {
		return Result{Valid: false, Code: "fail_empty_body", Reason: "Response body is empty or not an object"}
	}
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{Valid: false, Code: "fail_empty_body", Reason: "Response body is empty or not an object"}
	}
	if len(resp.Choices) == 0 {
		return Result{Valid: false, Code: "fail_no_choices", Reason: "Response has no choices array"}
	}
	first := resp.Choices[0]
	content := strings.TrimSpace(first.Message.Content)
	toolCalls := first.Message.ToolCalls
	if len(toolCalls) == 0 && content == "" {
		return Result{Valid: false, Code: "fail_empty_content", Reason: "Both content and tool_calls are empty"}
	}
	if content != "" {
		for _, pat := range refusalPatterns {
			if pat.MatchString(content) {
				return Result{Valid: false, Code: "fail_canned_error", Reason: "Response content matched upstream error/refusal pattern: " + pat.String()}
			}
		}
	}
	if len(toolCalls) > 0 {
		declared := map[string]bool{}
		for _, n := range declaredTools {
			declared[n] = true
		}
		for _, call := range toolCalls {
			if call.Function.Name == "" {
				return Result{Valid: false, Code: "fail_tool_shape", Reason: "tool_call missing function.name"}
			}
			if len(declared) > 0 && !declared[call.Function.Name] {
				return Result{Valid: false, Code: "fail_tool_unknown", Reason: "tool_call references undeclared function name: " + call.Function.Name}
			}
			if call.Function.Arguments != "" {
				var parsed interface{}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &parsed); err != nil {
					return Result{Valid: false, Code: "fail_tool_invalid_json_args", Reason: "tool_call arguments is invalid JSON"}
				}
				if _, ok := parsed.(map[string]interface{}); !ok {
					return Result{Valid: false, Code: "fail_tool_args_not_object", Reason: "tool_call arguments did not parse to a JSON object"}
				}
			}
		}
	}
	if responseFormatType == "json_object" || responseFormatType == "json_schema" {
		if content == "" {
			return Result{Valid: false, Code: "fail_json_empty", Reason: "JSON mode requested but content is empty"}
		}
		var js json.RawMessage
		if err := json.Unmarshal([]byte(content), &js); err != nil {
			return Result{Valid: false, Code: "fail_invalid_json_content", Reason: "JSON mode requested but content failed JSON.parse"}
		}
	}
	if first.FinishReason == "length" && (content == "" || len(content) < 50) {
		return Result{Valid: false, Code: "fail_truncated_length", Reason: "Response hit max_tokens length cut-off prematurely"}
	}
	return Result{Valid: true, Code: "pass"}
}
