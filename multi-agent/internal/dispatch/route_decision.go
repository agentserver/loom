// Package dispatch's route_decision.go implements the routing-trace path
// specified by docs/specs/wt1-routing-trace.spec.md (WT-1-routing-trace).
//
// The Dispatcher emits one RouteDecision per Run via a pluggable Writer.
// All forgery-shield logic (seed-pair pattern, FinalizeAndEmit overwrite,
// ReasonText sanitize, monotonic-clock duration, write-failure logging) is
// enforced here so the dispatch.go body stays mostly unchanged.
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"expvar"
	"log"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// ReasonCode enumerates the routing decision rationales. The string values are
// the persisted form in route_reasons.reason_code; downstream queries depend
// on them — do not change without a schema migration story.
type ReasonCode string

const (
	ReasonCapabilityMatch   ReasonCode = "capability_match"
	ReasonVersionTooOld     ReasonCode = "version_too_old"
	ReasonForbiddenCred     ReasonCode = "forbidden_cred"
	ReasonNotReachable      ReasonCode = "not_reachable"
	ReasonLoadTooHigh       ReasonCode = "load_too_high"
	ReasonNoCapabilityMatch ReasonCode = "no_capability_match"
	ReasonUnknown           ReasonCode = "unknown"
)

// Candidate is one row in RouteDecision.Candidates. JSON tags are the
// authoritative form for route_reasons.candidates_json; the field set is
// frozen at {agent_id, score, reason} per spec §6 (b).
type Candidate struct {
	AgentID string     `json:"agent_id"`
	Score   float64    `json:"score"`
	Reason  ReasonCode `json:"reason"`
}

// RouteDecision is the trace produced for every Dispatcher.Run. See
// docs/specs/wt1-routing-trace.spec.md §2.
type RouteDecision struct {
	// Caller-mutable fields populated between NewDecision and FinalizeAndEmit.
	Candidates      []Candidate
	SelectedAgentID string
	SelectedNone    bool // true iff no executor matched at all
	ReasonCode      ReasonCode
	ReasonText      string // sanitized inside FinalizeAndEmit

	// Read-only mirrors of the unexported canonical seed. Any caller mutation
	// to these is wiped inside FinalizeAndEmit.
	ConversationID     string
	DecisionID         string
	DecisionStartedAt  time.Time
	DecisionEndedAt    time.Time
	DecisionDurationNs int64

	// Unexported canonical seed. Only NewDecision writes. Callers outside the
	// dispatch package cannot construct a forged seed via struct literal
	// because these fields are unexported.
	seedConv    string
	seedStarted time.Time
	seedNonce   uint64
}

// decisionNonce is the process-local monotonic counter mixed into deriveID so
// two NewDecision calls landing on the same nanosecond timestamp still
// produce distinct DecisionIDs.
var decisionNonce atomic.Uint64

// NewDecision is the only constructor that produces a RouteDecision with a
// non-zero DecisionID. The (conversationID, time.Now(), nonce) seed is
// captured in unexported fields; FinalizeAndEmit re-applies it before
// serialization so any mid-flight caller mutation of the exported mirrors is
// silently reversed.
func NewDecision(conversationID string) *RouteDecision {
	t := time.Now()
	n := decisionNonce.Add(1)
	d := &RouteDecision{
		ConversationID:    conversationID,
		DecisionStartedAt: t,
		DecisionID:        deriveID(conversationID, t, n),
		seedConv:          conversationID,
		seedStarted:       t,
		seedNonce:         n,
	}
	return d
}

func deriveID(conv string, t time.Time, n uint64) string {
	sum := sha256.Sum256([]byte(
		conv + "|" + strconv.FormatInt(t.UnixNano(), 10) + "|" + strconv.FormatUint(n, 10),
	))
	return hex.EncodeToString(sum[:16])
}

// FinalizeAndEmit is called via defer at the top of Dispatcher.Run. It (a)
// overwrites the exported mirror fields from the unexported seed so caller
// mutation is wiped, (b) stamps the monotonic end timestamp + duration, (c)
// sanitizes ReasonText, (d) writes through the active writer using a context
// detached from the parent (so shutdown-cancelled parent ctx still records
// the trace), and (e) logs but does not propagate writer errors.
//
// The `parentCtx context.Context` parameter is INTENTIONALLY IGNORED
// (named `_` at the signature). It is kept in the signature so callers
// can pass their `ctx` idiomatically — the value's only purpose is
// documentation of the call-site scope. The writer call uses a fresh
// `context.WithTimeout(context.Background(), 2*time.Second)` so a
// shutdown-cancelled parent context cannot drop the trace. Removing
// the parameter would break the `defer FinalizeAndEmit(ctx, dec)`
// pattern at every Dispatcher.Run call site. See spec §6.
func FinalizeAndEmit(_ context.Context, d *RouteDecision) {
	if d == nil {
		return
	}
	end := time.Now()
	d.ConversationID = d.seedConv
	d.DecisionStartedAt = d.seedStarted
	d.DecisionEndedAt = end
	d.DecisionDurationNs = end.Sub(d.seedStarted).Nanoseconds()
	d.DecisionID = deriveID(d.seedConv, d.seedStarted, d.seedNonce)
	d.ReasonText = SanitizeReasonText(d.ReasonText)

	// Detach parent context: the trace is an audit artifact, not request-
	// scoped. Shutdown / parent-cancel must NOT drop the trace — that's
	// exactly when incident-reconstruction needs it most. 2 s ceiling caps
	// shutdown latency.
	writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := currentWriter().Write(writeCtx, *d); err != nil {
		log.Printf("[route-trace] write failed: %v conv=%s decision=%s",
			err, d.ConversationID, d.DecisionID)
	}
}

// ----------------------------------------------------------------------------
// Sanitize.

// secretBlacklistRE matches common API-token / credential / private-key
// shapes. The patterns are deliberately a bit broader than strict
// vendor-spec to catch test-shaped tokens too; false positives only cost
// a harmless [REDACTED] string in ReasonText.
//
// The pattern list is NOT exhaustive — sanitize is defence-in-depth, not
// the primary auth boundary. Adding a new family is a one-line change;
// the corresponding test case lives in TestSanitize_RawSecret_Redacted.
var secretBlacklistRE = regexp.MustCompile(
	// OpenAI / Anthropic and similar `sk-` family (covers `sk-ant-...`).
	`sk-[A-Za-z0-9_\-]{8,}|` +
		// JWT (header.payload.signature shape).
		`eyJ[A-Za-z0-9_\-\.]{16,}|` +
		// AWS access key.
		`AKIA[A-Z0-9]{12,}|` +
		// GitHub legacy/PAT/server/refresh/user/app tokens.
		`gh[opsruA-Z]_[A-Za-z0-9]{20,}|` +
		// GitHub fine-grained personal access tokens.
		`github_pat_[A-Za-z0-9_]{20,}|` +
		// GitLab personal access tokens.
		`glpat-[A-Za-z0-9_\-]{20,}|` +
		// Google API keys.
		`AIza[A-Za-z0-9_\-]{20,}|` +
		// Slack tokens.
		`xox[baprs]-[A-Za-z0-9-]{8,}|` +
		// PEM-armored private keys.
		`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
)

// routeReasonRedactedTotal counts ReasonText values that matched the secret
// blacklist. Exposed via expvar for "did sanitize ever fire?" smoke checks.
var routeReasonRedactedTotal = expvar.NewInt("route_reason_redacted_total")

// SanitizeReasonText replaces any value matching the secret blacklist with
// "[REDACTED]" and truncates the result to ≤ 256 runes (appending the literal
// suffix "...[truncated]" on overflow). Always safe to call; no-op on empty
// input. Spec §6 (a).
func SanitizeReasonText(s string) string {
	if s == "" {
		return s
	}
	redacted := false
	out := secretBlacklistRE.ReplaceAllStringFunc(s, func(_ string) string {
		redacted = true
		return "[REDACTED]"
	})
	if redacted {
		routeReasonRedactedTotal.Add(1)
	}
	runes := []rune(out)
	if len(runes) > 256 {
		return string(runes[:256]) + "...[truncated]"
	}
	return out
}

// ----------------------------------------------------------------------------
// Writer + thread-safe wiring.

// Writer persists one RouteDecision per call. Implementations must be
// goroutine-safe.
type Writer interface {
	Write(ctx context.Context, d RouteDecision) error
}

type noopWriter struct{}

func (noopWriter) Write(_ context.Context, _ RouteDecision) error { return nil }

// writerBox is the fixed concrete type stored in activeWriter so atomic.Value
// never sees varying concrete types (which would panic). The interface value
// inside may be any Writer.
type writerBox struct{ w Writer }

var activeWriter atomic.Value // always holds writerBox{...}

func init() { activeWriter.Store(writerBox{w: noopWriter{}}) }

// SetWriter installs w as the package-wide route trace writer. Passing
// nil — or a typed-nil pointer wrapped in the Writer interface (e.g.
// `var w *observerWriterAdapter; SetWriter(w)`) — resets to the noop
// writer. Without the reflect-based typed-nil check, a caller who
// accidentally hands in an uninitialized interface value would end up
// with a non-noop Writer whose method calls would panic on the first
// Write; IsNoopWriter would also return false, defeating the boot-time
// misconfig assertion documented in spec §6 (d).
func SetWriter(w Writer) {
	if w == nil || isNilInterface(w) {
		w = noopWriter{}
	}
	activeWriter.Store(writerBox{w: w})
}

// isNilInterface reports whether v is a non-nil interface value wrapping
// a nil pointer / chan / map / slice / func. Standard Go "typed nil in
// interface" gotcha — the outer interface holds a type tag so `v == nil`
// is false even though calling a method on v would panic.
func isNilInterface(v Writer) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Chan, reflect.Map, reflect.Slice, reflect.Func:
		return rv.IsNil()
	}
	return false
}

func currentWriter() Writer { return activeWriter.Load().(writerBox).w }

// IsNoopWriter reports whether the package-wide writer is the default noop.
// The slave-agent boot path is required to assert this returns false after
// calling SetWriter so misconfiguration is loud — see spec §6 (d).
func IsNoopWriter() bool {
	_, ok := currentWriter().(noopWriter)
	return ok
}

// ----------------------------------------------------------------------------
// observerstore bridge.

// WrapRouteWriter adapts an observerstore.RouteWriter into a dispatch.Writer.
// Slave-agent boot calls
//
//	dispatch.SetWriter(dispatch.WrapRouteWriter(observerstore.NewRouteWriter(db)))
//
// — that wiring lives in cmd/slave-agent (out of this WT's file domain;
// see spec §6 (d)).
func WrapRouteWriter(w observerstore.RouteWriter) Writer {
	return &observerWriterAdapter{w: w}
}

type observerWriterAdapter struct{ w observerstore.RouteWriter }

func (a *observerWriterAdapter) Write(ctx context.Context, d RouteDecision) error {
	cands := make([]observerstore.RouteCandidate, len(d.Candidates))
	for i, c := range d.Candidates {
		cands[i] = observerstore.RouteCandidate{
			AgentID: c.AgentID,
			Score:   c.Score,
			Reason:  string(c.Reason),
		}
	}
	selected := d.SelectedAgentID
	if d.SelectedNone {
		// Sentinel disambiguates "no candidate matched" from
		// "fallback executor (key="") was selected on purpose". Spec §3.1.
		selected = "<none>"
	}
	return a.w.WriteRouteReason(ctx, observerstore.RouteReasonRow{
		DecisionID:         d.DecisionID,
		ConversationID:     d.ConversationID,
		SelectedAgentID:    selected,
		ReasonCode:         string(d.ReasonCode),
		ReasonText:         d.ReasonText,
		Candidates:         cands,
		DecisionStartedAt:  d.DecisionStartedAt,
		DecisionEndedAt:    d.DecisionEndedAt,
		DecisionDurationNs: d.DecisionDurationNs,
	})
}

// ----------------------------------------------------------------------------
// peekConversationID — in-package envelope conversation_id extractor.

// convIDRE pulls the conversation_id JSON field out of a TASK_CONTRACT
// envelope. The pattern intentionally tolerates JSON-escaped quotes inside the
// value (`\"`) since the contract package's JSON encoder produces them. It
// does NOT validate the rest of the envelope — that's contract.DecodeEnvelope's
// job, called downstream. This is best-effort: any parse miss returns "" and
// the caller substitutes t.ID.
var convIDRE = regexp.MustCompile(`"conversation_id"\s*:\s*"((?:[^"\\]|\\.)*)"`)

func peekConversationID(prompt string) string {
	if !strings.Contains(prompt, contract.EnvelopeStart) {
		return ""
	}
	m := convIDRE.FindStringSubmatch(prompt)
	if len(m) < 2 {
		return ""
	}
	// Undo simple JSON escapes (just `\"` → `"` here — sufficient for the
	// shapes we receive; full JSON unescape is contract.DecodeEnvelope's job).
	return unescapeJSONStringMinimal(m[1])
}

func unescapeJSONStringMinimal(s string) string {
	if !containsBackslash(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"', '\\', '/':
				out = append(out, s[i+1])
				i++
				continue
			case 'n':
				out = append(out, '\n')
				i++
				continue
			case 't':
				out = append(out, '\t')
				i++
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func containsBackslash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			return true
		}
	}
	return false
}
