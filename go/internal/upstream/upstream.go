// Package upstream ports server/adapters.ts error normalisation,
// timeout tiers, cold-start detection, and Retry-After parsing.
package upstream

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Category string

const (
	CategoryServerError      Category = "server_error"
	CategoryRateLimited      Category = "rate_limited"
	CategoryQuotaExhausted   Category = "quota_exhausted"
	CategoryAuthInvalid      Category = "auth_invalid"
	CategoryModelForbidden   Category = "model_forbidden"
	CategoryTimeout          Category = "timeout"
	CategoryContextOverflow  Category = "context_overflow"
	CategoryContentFiltered  Category = "content_filtered"
	CategoryBadRequest       Category = "bad_request"
	CategoryUnsupportedParam Category = "unsupported_param"
)

type Error struct {
	Status       int
	Category     Category
	IsDailyQuota bool
	RetryAfter   *int
	Message      string
}

func (e *Error) Error() string { return e.Message }

func isKaggleProxy(baseURL string) bool {
	return strings.Contains(strings.ToLower(baseURL), "mp-staging.kaggle.net")
}

// IsColdStart ports isKaggleColdStartRoute exactly.
func IsColdStart(routeID, name, modelID, upstreamModel, baseURL string) bool {
	if isKaggleProxy(baseURL) {
		return false
	}
	combined := strings.ToLower(routeID + " " + name + " " + modelID + " " + upstreamModel + " " + strings.ToLower(baseURL))
	for _, pat := range []string{"qwen3.6", "kaggle", "trycloudflare", "ngrok", "gradio", "zrok", "pinggy", "llama-server", "localhost:8000", "127.0.0.1:8000"} {
		if strings.Contains(combined, pat) {
			return true
		}
	}
	return false
}

type TimeoutTier struct {
	TimeoutMs   int
	IsColdStart bool
	Tier        string // fast|reasoning|cold_start
}

// TimeoutTier ports getRouteTimeoutTier.
func TimeoutTierFor(routeID, name, modelID, upstreamModel, baseURL, taskRole string, streaming bool) TimeoutTier {
	if IsColdStart(routeID, name, modelID, upstreamModel, baseURL) {
		return TimeoutTier{TimeoutMs: 120000, IsColdStart: true, Tier: "cold_start"}
	}
	if taskRole == "fast" {
		if streaming {
			return TimeoutTier{TimeoutMs: 15000, Tier: "fast"}
		}
		return TimeoutTier{TimeoutMs: 60000, Tier: "fast"}
	}
	if streaming {
		return TimeoutTier{TimeoutMs: 45000, Tier: "reasoning"}
	}
	return TimeoutTier{TimeoutMs: 60000, Tier: "reasoning"}
}

// ParseRetryAfter ports parseRetryAfterSeconds (seconds or HTTP date,
// clamped 1-3600; x-ratelimit-reset-* duration or epoch).
func ParseRetryAfter(h http.Header, now time.Time) *int {
	clamp := func(v int) *int {
		if v < 1 {
			v = 1
		}
		if v > 3600 {
			v = 3600
		}
		return &v
	}
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return clamp(n)
		}
		if t, err := http.ParseTime(v); err == nil {
			diff := int(time.Until(t).Seconds()) + 1
			if diff > 0 {
				return clamp(diff)
			}
		}
	}
	for _, k := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens", "x-ratelimit-reset"} {
		v := h.Get(k)
		if v == "" {
			continue
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			if n > 1700000000 {
				diff := int(n - float64(now.Unix()))
				if diff > 0 {
					return clamp(diff)
				}
			} else {
				return clamp(int(n))
			}
		}
	}
	_ = now
	return nil
}

var contextOverflowMarkers = []string{"context_length_exceeded", "maximum context length", "context window", "token limit", "max_tokens"}

// Normalize ports normalizeUpstreamError. errText is the full upstream
// body; the returned Message truncates it to 200 chars like Node.
func Normalize(status int, errText string, headers http.Header, errorParam, errorCode string) *Error {
	lower := strings.ToLower(errText)
	msg := errText
	if len(msg) > 200 {
		msg = msg[:200]
	}
	e := &Error{Status: status, Category: CategoryServerError, Message: "Upstream returned HTTP " + strconv.Itoa(status) + ": " + msg}
	switch {
	case status == 401:
		e.Category = CategoryAuthInvalid
	case status == 402 || errorCode == "insufficient_quota" || strings.Contains(lower, "insufficient_quota") || strings.Contains(lower, "1113") || strings.Contains(lower, "balance") || strings.Contains(lower, "arrears") || strings.Contains(lower, "quota exceeded"):
		e.Category = CategoryQuotaExhausted
	case status == 403:
		e.Category = CategoryModelForbidden
	case status == 429:
		e.Category = CategoryRateLimited
		if strings.Contains(lower, "day") || strings.Contains(lower, "rpd") || strings.Contains(lower, "daily limit") {
			e.IsDailyQuota = true
		}
	case status == 413 && (strings.Contains(lower, "tokens per minute") || strings.Contains(lower, "tpm") || strings.Contains(lower, "rate limit")):
		e.Category = CategoryRateLimited
	case status == 413 || containsAny(lower, contextOverflowMarkers):
		e.Category = CategoryContextOverflow
	case strings.Contains(lower, "content filter") || strings.Contains(lower, "content_filtered") || strings.Contains(lower, "safety"):
		e.Category = CategoryContentFiltered
	case status == 400:
		if errorParam == "tools" || errorParam == "tool_choice" || errorParam == "response_format" || errorParam == "functions" || strings.Contains(lower, "unsupported") {
			e.Category = CategoryUnsupportedParam
		} else {
			e.Category = CategoryBadRequest
		}
	case status >= 500:
		e.Category = CategoryServerError
	case status == 408 || strings.Contains(lower, "timeout"):
		e.Category = CategoryTimeout
	}
	if headers != nil {
		e.RetryAfter = ParseRetryAfter(headers, time.Now())
	}
	return e
}

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
