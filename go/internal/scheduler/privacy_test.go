package scheduler

import "testing"

func msg(text string) Message { return Message{Role: "user", Content: text} }

func TestEffectivePrivacyMode(t *testing.T) {
	cases := map[string]string{
		"off": "off", "low": "low", "high": "high",
		"": "off", "invalid": "off", "HIGH": "high", " Low ": "low",
	}
	for in, want := range cases {
		if got := EffectivePrivacyMode(in); got != want {
			t.Fatalf("EffectivePrivacyMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrivacyOffIgnoresEverything(t *testing.T) {
	cls := Classify(ClassifyInput{
		PrivacyMode: "off", PrivateHeader: "true", Model: "auto:private",
		Messages: []Message{msg("my api_secret is here and binance strategy")},
	})
	if cls.IsPrivate {
		t.Fatalf("mode off must never mark private")
	}
}

func TestPrivacyInvalidModeIsOff(t *testing.T) {
	cls := Classify(ClassifyInput{
		PrivacyMode: "banana", Messages: []Message{msg("api_secret")},
	})
	if cls.IsPrivate {
		t.Fatalf("invalid mode must fail closed to off, not to high")
	}
}

func TestPrivacyLowKeyClassesOnly(t *testing.T) {
	// Low: key classes trigger; exchange names and strategy words do not.
	for _, text := range []string{"api_secret", "api-secret", "private_key", "private-key", "auth_token"} {
		cls := Classify(ClassifyInput{PrivacyMode: "low", Messages: []Message{msg(text)}})
		if !cls.IsPrivate {
			t.Fatalf("low must trigger on %q", text)
		}
	}
	for _, text := range []string{"binance", "trade_strategy", "backtest_portfolio", "okx"} {
		cls := Classify(ClassifyInput{PrivacyMode: "low", Messages: []Message{msg(text)}})
		if cls.IsPrivate {
			t.Fatalf("low must NOT trigger on %q", text)
		}
	}
}

func TestPrivacyHighFullList(t *testing.T) {
	for _, text := range []string{"binance", "okx", "bybit", "coinbase", "alpaca", "ibkr", "trade_strategy", "alpha_factor", "backtest_portfolio"} {
		cls := Classify(ClassifyInput{PrivacyMode: "high", Messages: []Message{msg(text)}})
		if !cls.IsPrivate {
			t.Fatalf("high must trigger on %q", text)
		}
	}
}

func TestPrivacyBearerLengthGate(t *testing.T) {
	// Node: bearer + 20+ token chars. A short "bearer x" must not trigger.
	cls := Classify(ClassifyInput{PrivacyMode: "high", Messages: []Message{msg("use bearer abc")}})
	if cls.IsPrivate {
		t.Fatalf("short bearer must not trigger")
	}
	cls = Classify(ClassifyInput{PrivacyMode: "high", Messages: []Message{msg("Authorization: Bearer abcdefghij1234567890")}})
	if !cls.IsPrivate {
		t.Fatalf("long bearer token must trigger")
	}
}

func TestPrivacyExplicitMarkers(t *testing.T) {
	cls := Classify(ClassifyInput{PrivacyMode: "high", PrivateHeader: "true", Messages: []Message{msg("hello")}})
	if !cls.IsPrivate {
		t.Fatalf("private header must trigger when mode is on")
	}
	cls = Classify(ClassifyInput{PrivacyMode: "high", Model: "auto:private", Messages: []Message{msg("hello")}})
	if !cls.IsPrivate {
		t.Fatalf("auto:private model must trigger when mode is on")
	}
}

func TestPrivacyArrayContentNotSampled(t *testing.T) {
	// Array content is not string content in Node, so it is not sampled.
	cls := Classify(ClassifyInput{
		PrivacyMode: "high",
		Messages: []Message{{Role: "user", Content: []ContentPart{{Type: "text", Text: "api_secret"}}}},
	})
	if cls.IsPrivate {
		t.Fatalf("array content must not be privacy-sampled")
	}
}
