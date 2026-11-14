// Package scheduler ports server/scheduler.ts (Node Railways) to Go.
// It is deliberately pure: no DB access here except through the
// interfaces the gateway supplies (quota counters, metrics, affinity).
// DB-backed auto-heal side effects live in the store layer.
package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Types mirror server/types.ts (JSON names are camelCase on the wire,
// snake_case in SQLite; this package uses Go field names).
type ProviderKey struct {
	ID             string
	Name           string
	ProviderType   string
	EncryptedKey   string
	KeyMask        string
	KeyState       string // healthy|cooldown|exhausted|disabled|unconfigured
	CooldownUntil  string
	Consecutive429 int
	BaseURL        string
	AccountNotes   string
	AppealURL      string
	CreatedAt      string
}

type Route struct {
	ID                  string
	Name                string
	ProviderKeyID       string
	ModelID             string
	DisplayName         string
	BaseURL             string
	ProviderType        string
	UpstreamModel       string
	TaskRole            string // auto|vision|reasoning|fast
	RouteState          string // healthy|degraded|unavailable_for_key|disabled
	CircuitState        string // closed|open|half_open
	CircuitOpensUntil   string
	ConsecutiveTimeouts int
	Consecutive5xx      int
	ConsecutiveFailures int
	CircuitOpenCount    int
	SupportsVision      bool
	SupportsTools       bool
	SupportsJSONMode    bool
	SupportsStreaming   bool
	MaxContext          int
	AllowSensitive      bool
	PrivateOnly         bool
	PrivacyTier         string
	CostWeight          float64
	ReliabilityScore    int
	Enabled             bool
	UnavailableUntil    string
	CreatedAt           string
}

type Metrics struct {
	LatencyP50    int
	LatencyP95    int
	SuccessCount  int
	FailureCount  int
	LastErrorCode string
}

type Quota struct {
	DailyTokenLimit  *int
	BilledTokensToday int
	FallbackTokensToday int
}

type SchedulerConfig struct {
	DispatchMode  string `json:"dispatchMode"` // auto|pinned
	PinnedModelID string `json:"pinnedModelId"`
	WeightsJSON   string `json:"weights"`
	RateLimitRPM  int    `json:"rateLimitRpm"`
	PrivacyMode   string `json:"privacyMode"` // off|low|high
}

func (c SchedulerConfig) Weights() (sticky, role, rel, cost, lat float64) {
	sticky, role, rel, cost, lat = 0.35, 0.30, 0.20, 0.10, 0.05
	if c.WeightsJSON == "" {
		return
	}
	var w struct {
		WSticky float64 `json:"wSticky"`
		WRole   float64 `json:"wRole"`
		WRel    float64 `json:"wRel"`
		WCost   float64 `json:"wCost"`
		WLat    float64 `json:"wLat"`
	}
	if json.Unmarshal([]byte(c.WeightsJSON), &w) == nil {
		if w.WSticky != 0 || w.WRole != 0 || w.WRel != 0 || w.WCost != 0 || w.WLat != 0 {
			sticky, role, rel, cost, lat = w.WSticky, w.WRole, w.WRel, w.WCost, w.WLat
		}
	}
	return
}

// Message / tool shapes for classification (subset of OpenAI chat).
type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []ContentPart
}

// StringText returns the message content only when it is a plain
// string, matching Node's privacy sampling (messages with string
// content only). Array content (e.g. vision messages with text +
// image parts) contributes nothing to privacy sampling in Node, even
// though Text() joins their text parts for other purposes.
func (m Message) StringText() string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	return ""
}

func (m Message) Text() string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []ContentPart:
		var b strings.Builder
		for _, p := range c {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func (m Message) HasImage() bool {
	parts, ok := m.Content.([]ContentPart)
	if !ok {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

type Classification struct {
	TaskType          string // vision|reasoning|fast
	RoutingMode       string // auto|pool
	IsPrivate         bool
	HasImage          bool
	HasTools          bool
	HasJSONMode       bool
	EstimatedTokens   int
	SessionKey        string
	IsExplicitSession bool
	RequestLanguage   string // chinese|english|neutral
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3000 && r <= 0x303F)
}

// EstimateTokens estimates request tokens from message text and tool
// definitions. Calibrated 2026-11-09 against live traffic: the old
// conservative factors (ascii 0.36, x1.25) overestimated code-heavy
// requests ~1.6x (real ~93k estimated as ~150k), which made HardFilter
// wrongly exclude 128k routes and thinned the pool to 2-3 routes.
// New factors keep a ~10% safety margin while tracking real usage;
// a low estimate only costs a context-overflow failover, while a high
// estimate wrongly shrinks the pool.
// ceil((CJK*0.75 + ascii*0.28 + toolsJSONLen*0.25) * 1.10).
func EstimateTokens(messages []Message, toolsJSONLen int) int {
	cjk, ascii := 0, 0
	for _, m := range messages {
		for _, r := range m.Text() {
			if isCJK(r) {
				cjk++
			} else {
				ascii++
			}
		}
	}
	overhead := 0.0
	if toolsJSONLen > 0 {
		overhead = float64(toolsJSONLen) * 0.25
	}
	return int(math.Ceil((float64(cjk)*0.75 + float64(ascii)*0.28 + overhead) * 1.10))
}

func DetectLanguage(messages []Message) string {
	cjk, letters := 0, 0
	for _, m := range messages {
		for _, r := range m.Text() {
			if isCJK(r) {
				cjk++
			} else if unicode.IsLetter(r) {
				letters++
			}
		}
	}
	// Node detectRequestLanguage thresholds (ported from scheduler.ts):
	// CJK-dominant -> chinese, otherwise letter-dominant -> english.
	if cjk > 0 && cjk*2 >= letters {
		return "chinese"
	}
	if letters > 0 {
		return "english"
	}
	return "neutral"
}

// ResolveRoutingMode ports server/modelRouting.ts.
func ResolveRoutingMode(model string) string {
	v := strings.TrimSpace(strings.ToLower(model))
	switch v {
	case "", "default", "auto", "auto:fast", "auto:reasoning", "auto:vision", "auto:private":
		return "auto"
	default:
		return "pool"
	}
}

// EffectivePrivacyMode normalizes a stored privacy mode to one of
// off/low/high. Only "low" and "high" pass through; everything else,
// including empty and invalid values, becomes "off".
//
// This is a deliberate, user-ordered divergence from Node: Node's
// getSchedulerConfig falls back to "high" for a missing/invalid value,
// and its DB schema default is "high". Live Railways runs with mode
// "off" by user order, and Go must fail closed to normal routing
// (off) rather than silently enabling private routing for an empty or
// invalid value. Migrated DBs that store "off" are unaffected either
// way. Callers must use this before Classify, and Classify applies it
// defensively as well.
func EffectivePrivacyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "low", "high":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "off"
	}
}

// Privacy patterns, ported exactly from Node scheduler.ts
// QUANT_SENSITIVE_PATTERNS (high) and QUANT_SENSITIVE_PATTERNS_LOW.
// High = the full list; low keeps only the key classes, so discussing
// exchange names or trading-strategy words no longer triggers private.
// Routing only: matching a pattern only changes which routes may be
// picked (allowSensitive/privateOnly hard filters). It is NOT
// encryption or redaction, and content is forwarded verbatim.
var privacyPatternsLow = []*regexp.Regexp{
	regexp.MustCompile(`(?i)api[_-]?secret`),
	regexp.MustCompile(`(?i)private[_-]?key`),
	regexp.MustCompile(`(?i)auth_token|bearer\s+[a-z0-9-_]{20,}`),
}

var privacyPatternsHigh = []*regexp.Regexp{
	regexp.MustCompile(`(?i)api[_-]?secret`),
	regexp.MustCompile(`(?i)private[_-]?key`),
	regexp.MustCompile(`(?i)binance|okx|bybit|coinbase|alpaca|ibkr`),
	regexp.MustCompile(`(?i)trade_strategy|alpha_factor|backtest_portfolio`),
	regexp.MustCompile(`(?i)auth_token|bearer\s+[a-z0-9-_]{20,}`),
}

// Classify ports classifyRequest, minus DB reads (privacyMode and
// session sources are supplied by the caller via ClassificationInput).
type ClassifyInput struct {
	TaskHeader    string
	PrivateHeader string
	Model         string
	Messages      []Message
	ToolsJSONLen  int
	HasTools      bool
	HasJSONMode   bool
	PrivacyMode   string
	HeaderSession string
	MetaSession   string
	RequestID     string
	// ClientTokenID is the authenticated portal token's ID (not the
	// secret value). It seeds the derived session key for clients
	// that send no session header or metadata.
	ClientTokenID string
}

func Classify(in ClassifyInput) Classification {
	taskHeader := strings.ToLower(strings.TrimSpace(in.TaskHeader))
	privateHeader := strings.ToLower(strings.TrimSpace(in.PrivateHeader))
	modelLower := strings.ToLower(strings.TrimSpace(in.Model))
	routingMode := ResolveRoutingMode(in.Model)
	hasImage := false
	for _, m := range in.Messages {
		if m.HasImage() {
			hasImage = true
		}
	}
	est := EstimateTokens(in.Messages, in.ToolsJSONLen)
	lang := DetectLanguage(in.Messages)
	isPrivate := false
	// Mode off ignores explicit markers and word lists alike, and the
	// request is scheduled normally, as in Node.
	if mode := EffectivePrivacyMode(in.PrivacyMode); mode != "off" {
		isPrivate = privateHeader == "true" || modelLower == "auto:private"
		if !isPrivate {
			var b strings.Builder
			for _, m := range in.Messages {
				b.WriteString(m.StringText())
				b.WriteByte(' ')
			}
			sampleText := b.String()
			patterns := privacyPatternsHigh
			if mode == "low" {
				patterns = privacyPatternsLow
			}
			for _, pat := range patterns {
				if pat.MatchString(sampleText) {
					isPrivate = true
					break
				}
			}
		}
	}
	sessionKey, explicit := "", false
	if strings.TrimSpace(in.HeaderSession) != "" {
		sessionKey, explicit = strings.TrimSpace(in.HeaderSession), true
	} else if strings.TrimSpace(in.MetaSession) != "" {
		sessionKey, explicit = strings.TrimSpace(in.MetaSession), true
	} else if key := derivedSessionKey(in.ClientTokenID, in.Messages); key != "" {
		// Clients that send no session (e.g. Roo) get a stable
		// derived key so affinity can stick a conversation to one
		// model. It is explicit: the affinity read/write paths
		// treat it exactly like a header session.
		sessionKey, explicit = key, true
	} else {
		sessionKey = "ephemeral_" + in.RequestID
	}
	cls := Classification{RoutingMode: routingMode, IsPrivate: isPrivate, HasImage: hasImage, HasTools: in.HasTools, HasJSONMode: in.HasJSONMode, EstimatedTokens: est, SessionKey: sessionKey, IsExplicitSession: explicit, RequestLanguage: lang}
	switch {
	case taskHeader == "vision" || modelLower == "auto:vision":
		cls.TaskType = "vision"
	case taskHeader == "reasoning" || modelLower == "auto:reasoning":
		cls.TaskType = "reasoning"
	case taskHeader == "fast" || modelLower == "auto:fast":
		cls.TaskType = "fast"
	case hasImage:
		cls.TaskType = "vision"
	case in.HasTools || in.HasJSONMode:
		cls.TaskType = "reasoning"
	case est > 3200:
		cls.TaskType = "reasoning"
	default:
		cls.TaskType = "fast"
	}
	return cls
}

// derivedSessionKey builds a stable session key for clients that send
// no session header or metadata. The key is the client token ID plus
// a SHA-256 hash of the first user message (coding clients resend the
// full history every turn, so the first user message is stable across
// turns of one conversation). Only the hash is ever stored or logged;
// the raw message text never leaves the request. Empty when there is
// no token ID or no user message with text, in which case the caller
// falls back to an ephemeral key.
func derivedSessionKey(clientTokenID string, msgs []Message) string {
	if strings.TrimSpace(clientTokenID) == "" {
		return ""
	}
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		text := strings.TrimSpace(m.Text())
		if text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		return "derived_" + clientTokenID + "_" + hex.EncodeToString(sum[:])[:16]
	}
	return ""
}

type SizeClass string

const (
	SizeSmall  SizeClass = "small"
	SizeMedium SizeClass = "medium"
	SizeLarge  SizeClass = "large"
)

func SizeClassOf(estimated int) SizeClass {
	if estimated <= 3200 {
		return SizeSmall
	}
	if estimated > 32000 {
		return SizeLarge
	}
	return SizeMedium
}

type ScoreBreakdown struct {
	OrderBase         float64 `json:"orderBase"`
	ReliabilityBonus  float64 `json:"reliabilityBonus"`
	LatencyPenalty    float64 `json:"latencyPenalty"`
	QuotaBonus        float64 `json:"quotaBonus"`
	FailedDemotion    float64 `json:"failedDemotion"`
	SizeClass         string  `json:"sizeClass"`
	ContextBonus      float64 `json:"contextBonus"`
	LargeGuardPenalty float64 `json:"largeGuardPenalty"`
	CapabilityBonus   float64 `json:"capabilityBonus"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// ComputeAliasScore ports computeAliasScore exactly, including the
// overview rule: callers pass IncludeCapability=false for display.
func ComputeAliasScore(targetIdx int, reliability *int, latencyP50 int, quota *Quota, verification string, maxContext int, baseURL string, estimated int, language string, routeTags []string, includeCapability bool) (float64, ScoreBreakdown) {
	size := SizeClassOf(estimated)
	orderGap := 100.0
	relMult, latMult := 10.0, 5.0
	if size == SizeSmall {
		orderGap, latMult = 50, 30
	}
	if size == SizeLarge {
		relMult = 30
	}
	relScore := 100
	if reliability != nil {
		relScore = *reliability
	}
	rRel := math.Max(0, math.Min(1, float64(relScore)/100))
	lLat := math.Max(0, math.Min(1, float64(latencyP50)/5000))
	quotaBonus := 10.0
	if quota != nil && quota.DailyTokenLimit != nil {
		lim := float64(*quota.DailyTokenLimit)
		if lim > 0 {
			quotaBonus = math.Max(0, (lim-float64(quota.BilledTokensToday))/lim*10)
		} else {
			quotaBonus = 0
		}
	}
	failed := 0.0
	if verification == "failed" {
		failed = 500
	}
	ctxBonus := 0.0
	if size == SizeLarge && maxContext >= 1000000 {
		ctxBonus = 25
	}
	guard := 0.0
	if size == SizeLarge && strings.Contains(baseURL, "api.groq.com") {
		guard = 200
	}
	capBonus := 0.0
	if includeCapability && language != "" && language != "neutral" {
		for _, t := range routeTags {
			if t == language {
				capBonus = 50
				break
			}
		}
	}
	bd := ScoreBreakdown{
		OrderBase: 1000 - float64(targetIdx)*orderGap, ReliabilityBonus: round1(rRel * relMult), LatencyPenalty: round1(lLat * latMult), QuotaBonus: round1(quotaBonus), FailedDemotion: failed, SizeClass: string(size), ContextBonus: ctxBonus, LargeGuardPenalty: guard, CapabilityBonus: capBonus,
	}
	score := round1(bd.OrderBase + bd.ReliabilityBonus - bd.LatencyPenalty + bd.QuotaBonus - bd.FailedDemotion + bd.ContextBonus - bd.LargeGuardPenalty + bd.CapabilityBonus)
	return score, bd
}

// EffectiveState returns the effective state WITHOUT DB auto-heal
// side effects; the store layer applies auto-heal (cooldown expiry,
// unavailable expiry, circuit open->half_open) before calling this.
// halfOpenLocked is the 30s probe lock state.
func EffectiveState(route Route, key *ProviderKey, halfOpenLocked bool) string {
	if !route.Enabled || route.RouteState == "disabled" {
		return "disabled"
	}
	if key == nil {
		return "unconfigured"
	}
	if key.KeyState == "disabled" {
		return "disabled"
	}
	if key.KeyState == "unconfigured" || strings.TrimSpace(key.EncryptedKey) == "" {
		return "unconfigured"
	}
	if key.KeyState == "cooldown" {
		return "cooldown"
	}
	if key.KeyState == "exhausted" {
		return "exhausted"
	}
	if route.RouteState == "unavailable_for_key" {
		return "unavailable_for_key"
	}
	if route.CircuitState == "open" {
		return "cooldown"
	}
	if route.CircuitState == "half_open" {
		if halfOpenLocked {
			return "cooldown"
		}
		return "degraded"
	}
	if route.RouteState == "degraded" {
		return "degraded"
	}
	return "healthy"
}

func VerificationStatus(route Route, key *ProviderKey, metrics Metrics, eff string) string {
	if key == nil || eff == "unconfigured" {
		return "unconfigured"
	}
	switch eff {
	case "disabled", "exhausted", "unavailable_for_key", "cooldown":
		return "failed"
	}
	if route.ConsecutiveFailures > 0 || metrics.LastErrorCode != "" {
		return "failed"
	}
	if metrics.SuccessCount > 0 {
		return "verified"
	}
	return "untested"
}

type Candidate struct {
	Route          Route
	Key            ProviderKey
	EffectiveState string
	Score          float64
	IsSticky       bool
	TargetIdx      int // -1 when not from an alias list
}

// HardFilter returns false when the candidate must be skipped.
func HardFilter(cls Classification, route Route, quota *Quota) bool {
	if cls.HasImage && !route.SupportsVision {
		return false
	}
	if cls.HasTools && !route.SupportsTools {
		return false
	}
	if cls.HasJSONMode && !route.SupportsJSONMode {
		return false
	}
	if cls.IsPrivate && !route.AllowSensitive {
		return false
	}
	if !cls.IsPrivate && route.PrivateOnly {
		return false
	}
	if route.MaxContext < cls.EstimatedTokens+1000 {
		return false
	}
	if quota != nil && quota.DailyTokenLimit != nil && quota.BilledTokensToday >= *quota.DailyTokenLimit {
		return false
	}
	return true
}

func rotationWeight(pos int) float64 {
	switch pos {
	case 0:
		return 4
	case 1:
		return 3
	case 2:
		return 2
	default:
		return 1
	}
}

// Rotation state is in-process, keyed by poolKey:sizeClass, exactly
// like Node rotationCurrents. Restart resets distribution only.
type Rotator struct {
	// WeightFn, when set, may replace the static position weights for one
	// selection. It is set once at startup, before requests are served, and
	// only read afterwards. pool is the filtered candidate list in position
	// order and static holds the static weight of each position (a copy the
	// callee may keep). Returning nil, a slice of the wrong length, any
	// non-finite or non-positive value, or panicking all make the rotator use
	// the static weights, so a faulty hook can never fail a request.
	WeightFn func(poolKey string, size SizeClass, pool []Candidate, static []float64) []float64

	mu       sync.Mutex
	currents map[string]map[string]float64
}

func NewRotator() *Rotator { return &Rotator{currents: map[string]map[string]float64{}} }

// customWeights runs WeightFn and returns its result only when it is usable,
// otherwise nil. It is called without holding r.mu.
func (r *Rotator) customWeights(poolKey string, size SizeClass, pool []Candidate, static []float64) (w []float64) {
	if r.WeightFn == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			w = nil
		}
	}()
	w = r.WeightFn(poolKey, size, pool, append([]float64(nil), static...))
	if len(w) != len(pool) {
		return nil
	}
	for _, x := range w {
		if math.IsNaN(x) || math.IsInf(x, 0) || x <= 0 {
			return nil
		}
	}
	return w
}

const KaggleRotationShareCap = 0.60

// ReorderForRotation ports reorderCandidatesForRotation.
// kaggleModelID is the Kaggle main route modelId; empty disables the cap.
// billedByKey maps provider key ID -> billed tokens today for the pool.
// It returns [picked, ...rest in original order].
func (r *Rotator) ReorderForRotation(candidates []Candidate, poolKey string, size SizeClass, kaggleModelID string, billedByKey map[string]int) []Candidate {
	if len(candidates) <= 1 {
		return candidates
	}
	pool := append([]Candidate{}, candidates...)
	if size == SizeLarge {
		filtered := make([]Candidate, 0, len(pool))
		for _, c := range pool {
			if !strings.Contains(c.Route.BaseURL, "api.groq.com") {
				filtered = append(filtered, c)
			}
		}
		pool = filtered
	}
	if kaggleModelID != "" {
		total, kaggle := 0, 0
		for _, c := range candidates {
			b := billedByKey[c.Key.ID]
			total += b
			if c.Route.ModelID == kaggleModelID {
				kaggle += b
			}
		}
		if total > 0 && float64(kaggle)/float64(total) > KaggleRotationShareCap {
			filtered := make([]Candidate, 0, len(pool))
			for _, c := range pool {
				if c.Route.ModelID != kaggleModelID {
					filtered = append(filtered, c)
				}
			}
			pool = filtered
		}
	}
	if len(pool) == 0 {
		return candidates
	}
	static := make([]float64, len(pool))
	for idx := range pool {
		static[idx] = rotationWeight(idx)
	}
	weights := static
	if w := r.customWeights(poolKey, size, pool, static); w != nil {
		weights = w
	}
	stateKey := poolKey + ":" + string(size)

	r.mu.Lock()
	currents, ok := r.currents[stateKey]
	if !ok {
		currents = map[string]float64{}
		r.currents[stateKey] = currents
	}
	// Clean up stale entries: remove route IDs that are no longer in the pool.
	// This prevents counters from accumulating for routes that have been
	// disabled, exhausted, or otherwise removed from rotation.
	alive := make(map[string]bool, len(pool))
	for _, c := range pool {
		alive[c.Route.ID] = true
	}
	for id := range currents {
		if !alive[id] {
			delete(currents, id)
		}
	}
	totalWeight := 0.0
	type scored struct {
		c   Candidate
		cur float64
		idx int
	}
	scoredList := make([]scored, 0, len(pool))
	for idx, c := range pool {
		w := weights[idx]
		totalWeight += w
		cur := currents[c.Route.ID] + w
		currents[c.Route.ID] = cur
		scoredList = append(scoredList, scored{c, cur, idx})
	}
	// Highest counter wins, ties by original scored order.
	best := scoredList[0]
	for _, s := range scoredList[1:] {
		if s.cur > best.cur {
			best = s
		}
	}
	currents[best.c.Route.ID] = currents[best.c.Route.ID] - totalWeight
	r.mu.Unlock()

	out := []Candidate{best.c}
	for _, c := range candidates {
		if c.Route.ID != best.c.Route.ID {
			out = append(out, c)
		}
	}
	return out
}
