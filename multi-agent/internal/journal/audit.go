// Package journal: WT-2-runtime-audit — runtime audit layer that records
// the actual read / write / tool_call / model_call events observed at
// the driver-executor seam, diffs them against the in-force
// TaskContract, and feeds sha256 write hashes to the D1 runs.artifact_hashes
// column via ArtifactHashAppender. Persisted through
// observerstore.AuditWriter into audit_events; surfaced through the
// contract_violations view.
//
// See docs/specs/wt2-runtime-audit.spec.md for the field-by-field
// contract and the Security (a)-(i) items the code implements.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"expvar"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// AuditKind enumerates the four observable effect classes. The values
// are the wire format persisted in audit_events.kind (schema.sql CHECK
// clause pins the same set — changing here is a schema break).
type AuditKind string

const (
	KindRead      AuditKind = "read"
	KindWrite     AuditKind = "write"
	KindToolCall  AuditKind = "tool_call"
	KindModelCall AuditKind = "model_call"
)

// AuditEvent is one observation of a runtime effect. Field set is fixed
// at spec §2.1; adding a field is a wire-compat break because the
// contract_violations view selects by column name.
type AuditEvent struct {
	// ConversationID pins the event to the TaskContract in force.
	// Empty is rejected — a runtime effect that cannot be tied to a
	// contract cannot participate in the diff.
	ConversationID string

	// RunID is the D1 run_id. Empty is permitted so the recorder
	// still works in unit tests / pre-eval-runner code paths; the
	// view LEFT-joins nothing (surfaces run_id as-is).
	RunID string

	// WorkspaceID scopes the (workspace, conversation) join key for
	// the view; empty is permitted (matches audit_events default).
	WorkspaceID string

	// Kind is one of the four constants above. Any other value is
	// rejected with ErrInvalidKind.
	Kind AuditKind

	// Target is:
	//   - for KindRead / KindWrite: an ABSOLUTE filesystem path.
	//     May contain sensitive substrings; ALWAYS re-scrubbed by
	//     Recorder.Record before insert (spec §7 (b)).
	//   - for KindToolCall: the tool name (e.g. "bash" or
	//     "mcp:my-server:list_files"); no arguments.
	//   - for KindModelCall: the model id (e.g. "glm-5.2"); no
	//     prompt.
	// Length capped at 4 KiB POST-scrub; anything longer returns
	// ErrTargetTooLong.
	Target string

	// Ts is a wall-clock time captured via time.Now (Go monotonic
	// clock is embedded). Zero is rejected.
	Ts time.Time

	// SizeBytes: KindWrite only. Non-zero on non-write kinds is
	// rejected.
	SizeBytes int64

	// Hash: KindWrite only. Non-empty MUST match ^[a-f0-9]{64}$.
	// Non-empty on non-write kinds is rejected.
	Hash string
}

// Sentinel errors returned (wrapped via fmt.Errorf) by Record and
// NopArtifactHashAppender.Append. Callers MUST test with errors.Is.
var (
	ErrInvalidKind           = errors.New("journal: AuditEvent.Kind not one of read|write|tool_call|model_call")
	ErrEmptyConversationID   = errors.New("journal: AuditEvent.ConversationID is empty")
	ErrInvalidTimestamp      = errors.New("journal: AuditEvent.Ts is zero")
	ErrTargetTooLong         = errors.New("journal: AuditEvent.Target exceeds 4 KiB")
	ErrInvalidSizeOnNonWrite = errors.New("journal: AuditEvent.SizeBytes non-zero on non-write kind")
	ErrInvalidHashOnNonWrite = errors.New("journal: AuditEvent.Hash non-empty on non-write kind")
	// ErrInvalidArtifactHash matches the sentinel-value contract of
	// evalrun.ErrInvalidArtifactHash. The two are spelled the same
	// but defined independently to keep the import graph shallow
	// (evalrun already imports journal transitively via observerstore
	// would create a cycle otherwise).
	ErrInvalidArtifactHash = errors.New("journal: invalid artifact hash (must be sha256 hex ^[a-f0-9]{64}$)")
)

// maxTargetBytes caps Target after scrub. 4 KiB is one order of
// magnitude over any legitimate path and keeps a hostile caller from
// using audit_events as a general-purpose DB filler.
const maxTargetBytes = 4 * 1024

// sha256HexRe matches lowercase-only 64-char hex; UPPERCASE is
// intentionally rejected so downstream sort/dedupe of hashes never
// double-counts the same content under different case.
var sha256HexRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// sensitivePathRe is the spec §7 (b) sensitive-path pattern set. Any
// match is REDACTED down to the top-level marker so the audit trail
// still shows "some credential file was touched" without leaking the
// specific identity. Composed with secretscrub.Sanitize inside
// scrubTarget — see the two-step call there for the intended order.
//
// Alternation over: user home cred dirs (aws / ssh / gnupg / gcloud /
// docker / netrc / kube) + /etc/shadow + macOS /private/etc/shadow.
// Add a family: update this regex AND add a case to the
// TestScrubTarget_SensitivePaths table.
var sensitivePathRe = regexp.MustCompile(
	`(?:/home/[^/]+|/root|/Users/[^/]+)/\.aws(?:/[^\s]*)?` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.ssh(?:/[^\s]*)?` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.gnupg(?:/[^\s]*)?` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.config/gcloud(?:/[^\s]*)?` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.docker/config\.json` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.netrc` +
		`|(?:/home/[^/]+|/root|/Users/[^/]+)/\.kube/config` +
		`|/etc/shadow-?` +
		`|/private/etc/shadow-?`,
)

// sensitivePathReplacement is the placeholder inserted for every
// sensitive-path match. Keeps the "some credential file was touched"
// signal without leaking the specific path. Contains no substring the
// regex or secretscrub.Sanitize can match again — this keeps
// scrubTarget idempotent (see TestScrubTarget_Idempotent).
const sensitivePathReplacement = "<HOME>/[REDACTED-CRED-PATH]"

// auditWriteDroppedTotal counts every Recorder.Record call whose caller
// logged the write failure. Exported via AuditWriteDroppedTotal() so
// external tests can assert without touching a package-private symbol.
var auditWriteDroppedTotal = expvar.NewInt("audit_write_dropped_total")

// AuditWriteDroppedTotal returns the *expvar.Int published under
// "audit_write_dropped_total". Exported as a function (not a bare var)
// so the counter identity is stable across the follow-up wiring WT
// (spec §7 (a)) and so external test code has a stable name to bind
// against.
func AuditWriteDroppedTotal() *expvar.Int { return auditWriteDroppedTotal }

// auditTargetRedactedTotal counts every scrubTarget call that performed
// at least one redaction (either a secretscrub hit or a sensitive-path
// hit). Distinct from route_reason_redacted_total so operators can
// tell audit-side redactions from route-trace-side redactions.
var auditTargetRedactedTotal = expvar.NewInt("audit_target_redacted_total")

// AuditTargetRedactedTotal exposes the redaction counter with the same
// accessor-not-var rationale as AuditWriteDroppedTotal.
func AuditTargetRedactedTotal() *expvar.Int { return auditTargetRedactedTotal }

// scrubTarget applies the two-step scrub described in spec §7 (b):
//
//  1. secretscrub.Sanitize — redacts inline token shapes (sk-…, eyJ…,
//     AKIA…, gh?_…, github_pat_…, glpat-…, AIza…, xox?-…, PEM
//     headers). This runs FIRST so a token embedded in an otherwise
//     benign path is caught even if the sensitive-path regex would not
//     have matched.
//  2. sensitivePathRe — REDACTS any known credential-file path down to
//     the sensitivePathReplacement marker.
//
// Returns the scrubbed string and a bool indicating whether ANY
// redaction happened (used to bump the counter exactly once per call).
// scrubTarget is idempotent: scrubTarget(scrubTarget(x)) == scrubTarget(x)
// for every x. The idempotence guard is not explicit; it follows from
// the two facts that (a) secretscrub.Sanitize is idempotent, and (b)
// sensitivePathReplacement contains no substring sensitivePathRe or
// secretscrub can match (no "/home/" prefix, no token-shape).
func scrubTarget(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	// Step 1: token scrub. secretscrub already truncates to 256 runes,
	// so an audit_events row that survives step 1 is bounded on the
	// token side; step 2 may extend up to a 4-KiB path prefix, which
	// is handled by the maxTargetBytes cap after both steps run.
	step1 := secretscrub.Sanitize(s)
	// Step 2: sensitive path replacement.
	redactedPath := false
	step2 := sensitivePathRe.ReplaceAllStringFunc(step1, func(_ string) string {
		redactedPath = true
		return sensitivePathReplacement
	})
	changed := step2 != s
	if changed {
		auditTargetRedactedTotal.Add(1)
	}
	_ = redactedPath // path-hit bool retained for future per-family counters
	return step2, changed
}

// Recorder persists an AuditEvent to the audit_events table.
// Implementations MUST be safe for concurrent use — driver, executor,
// and slave-agent may all instrument the same run from different
// goroutines.
type Recorder interface {
	Record(ctx context.Context, ev AuditEvent) error
}

// NopRecorder swallows every event and returns nil. Zero-value default
// so a slave-agent main that has not yet wired an SQLite recorder does
// not have to guard every Record call.
type NopRecorder struct{}

// Record on NopRecorder is a no-op returning nil.
func (NopRecorder) Record(context.Context, AuditEvent) error { return nil }

// sqlRecorder is the production Recorder backed by observerstore.AuditWriter.
type sqlRecorder struct {
	writer observerstore.AuditWriter
	newID  func() (string, error)
}

// NewSQLRecorder returns a Recorder backed by *sql.DB. A nil db returns
// NopRecorder so the "observer DB not yet mounted" path is a no-op
// rather than a construction error (spec §2.2).
func NewSQLRecorder(db *sql.DB) Recorder {
	if db == nil {
		return NopRecorder{}
	}
	return &sqlRecorder{
		writer: observerstore.NewAuditWriter(db),
		newID:  observerstore.GeneratedEventID,
	}
}

// Record validates, scrubs, and inserts. See spec §2.2 for the full
// behaviour contract; §7 (b) for the scrub step; §7 (a) for the
// "return error, do NOT log, do NOT swallow" property (the caller is
// responsible for the log-and-continue wrapper).
func (r *sqlRecorder) Record(ctx context.Context, ev AuditEvent) error {
	// Validate first — even if scrub would clean the target, an
	// invalid Kind or a hash-on-non-write is a caller bug we want to
	// surface immediately.
	if err := validateEvent(ev); err != nil {
		return err
	}
	// Cap check runs BEFORE scrub so a hostile 100-MiB target is
	// rejected without paying the secretscrub regex sweep. Post-scrub
	// the effective ceiling is tighter (secretscrub.Sanitize truncates
	// to 256 runes), so the 4-KiB cap is the pre-scrub gate — see
	// spec §7 (b) for the pre-vs-post ordering rationale.
	if len(ev.Target) > maxTargetBytes {
		return fmt.Errorf("%w: len=%d cap=%d", ErrTargetTooLong, len(ev.Target), maxTargetBytes)
	}
	scrubbed, _ := scrubTarget(ev.Target)
	id, err := r.newID()
	if err != nil {
		return fmt.Errorf("journal: generate event_id: %w", err)
	}
	row := observerstore.AuditEventRow{
		EventID:        id,
		WorkspaceID:    ev.WorkspaceID,
		ConversationID: ev.ConversationID,
		RunID:          ev.RunID,
		Kind:           string(ev.Kind),
		Target:         scrubbed,
		SizeBytes:      ev.SizeBytes,
		Hash:           ev.Hash,
		TS:             ev.Ts.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	return r.writer.WriteAuditEvent(ctx, row)
}

// validateEvent enforces the §2.1 invariants that must hold BEFORE
// scrub. Scrub-length is checked in Record itself because it depends
// on the post-scrub string.
func validateEvent(ev AuditEvent) error {
	if ev.ConversationID == "" {
		return ErrEmptyConversationID
	}
	switch ev.Kind {
	case KindRead, KindWrite, KindToolCall, KindModelCall:
	default:
		return fmt.Errorf("%w: got %q", ErrInvalidKind, string(ev.Kind))
	}
	if ev.Ts.IsZero() {
		return ErrInvalidTimestamp
	}
	if ev.Kind != KindWrite {
		if ev.SizeBytes != 0 {
			return fmt.Errorf("%w: kind=%s size=%d", ErrInvalidSizeOnNonWrite, ev.Kind, ev.SizeBytes)
		}
		if ev.Hash != "" {
			return fmt.Errorf("%w: kind=%s hash=%q", ErrInvalidHashOnNonWrite, ev.Kind, ev.Hash)
		}
	} else if ev.Hash != "" && !sha256HexRe.MatchString(ev.Hash) {
		return fmt.Errorf("%w: hash=%q", ErrInvalidArtifactHash, ev.Hash)
	}
	// Pre-scrub Target length is unbounded; the post-scrub cap in
	// Record is the real gate. This lets a caller-facing test that
	// injects a 100-KiB Target still surface ErrTargetTooLong from
	// Record instead of a scrub-happens-first-so-error-changes bug.
	return nil
}

// ViolationKind names the four undeclared-effect classes. Values match
// the CASE arms in the contract_violations view (spec §4).
type ViolationKind string

const (
	ViolationUndeclaredRead      ViolationKind = "undeclared_read"
	ViolationUndeclaredWrite     ViolationKind = "undeclared_write"
	ViolationUndeclaredToolCall  ViolationKind = "undeclared_tool_call"
	ViolationUndeclaredModelCall ViolationKind = "undeclared_model_call"
)

// Violation is one row of the actual-vs-contract diff. Emitted by
// Verifier.Verify AND surfaced by the contract_violations view — the
// two projections agree on column names.
type Violation struct {
	ConversationID string
	RunID          string
	Kind           ViolationKind
	Target         string
	Expected       string // JSON-encoded declared set (may be "")
	Observed       string
	Ts             time.Time
}

// Verifier is a pure function (no I/O; no globals read or written; no
// goroutines). Given a contract and events tied to the same
// conversation, it returns the violation list in event-Ts ascending
// order. Empty result → returns a non-nil empty slice.
type Verifier interface {
	Verify(c contract.TaskContract, events []AuditEvent) []Violation
}

type verifier struct{}

// NewVerifier returns the stateless verifier. Repeated calls return
// the same value (there is nothing to configure).
func NewVerifier() Verifier { return verifier{} }

// Verify implements the §2.3 rules. NO os / net / sql / http / time.Now
// / os.Getenv / log / expvar calls — enforced by TestVerifier_PurityGrep.
func (verifier) Verify(c contract.TaskContract, events []AuditEvent) []Violation {
	if len(events) == 0 {
		return []Violation{}
	}
	// Precompute the declared sets once, not per-event. Small map
	// lookups beat linear scans on any contract with >1 declared
	// entry.
	readSet := make(map[string]struct{}, len(c.DataContract.ReadArtifacts)*2)
	for _, r := range c.DataContract.ReadArtifacts {
		if r.Name != "" {
			readSet[r.Name] = struct{}{}
		}
		if r.ArtifactID != "" {
			readSet[r.ArtifactID] = struct{}{}
		}
	}
	writeSet := make(map[string]struct{}, len(c.DataContract.WriteTargets))
	for _, w := range c.DataContract.WriteTargets {
		if w.Name != "" {
			writeSet[w.Name] = struct{}{}
		}
	}
	toolSet := make(map[string]struct{}, len(c.CapabilityRequirements.Tools))
	for _, t := range c.CapabilityRequirements.Tools {
		toolSet[t] = struct{}{}
	}
	// Model requirements ride on skills today (spec §2.3 row 4).
	modelSet := make(map[string]struct{}, len(c.CapabilityRequirements.Skills))
	for _, s := range c.CapabilityRequirements.Skills {
		modelSet[s] = struct{}{}
	}

	out := make([]Violation, 0)
	for _, ev := range events {
		// Skip events that failed pre-validation semantics inline
		// (Kind is unknown). Verify is called on in-DB events which
		// have already passed Recorder validation, but we defend
		// against fuzz-driven bad inputs too.
		if ev.Kind != KindRead && ev.Kind != KindWrite && ev.Kind != KindToolCall && ev.Kind != KindModelCall {
			continue
		}
		var (
			declared    map[string]struct{}
			kind        ViolationKind
			expectedRaw string
		)
		switch ev.Kind {
		case KindRead:
			declared = readSet
			kind = ViolationUndeclaredRead
			expectedRaw = keysCSV(readSet)
		case KindWrite:
			declared = writeSet
			kind = ViolationUndeclaredWrite
			expectedRaw = keysCSV(writeSet)
		case KindToolCall:
			declared = toolSet
			kind = ViolationUndeclaredToolCall
			expectedRaw = keysCSV(toolSet)
		case KindModelCall:
			declared = modelSet
			kind = ViolationUndeclaredModelCall
			expectedRaw = keysCSV(modelSet)
		}
		if _, ok := declared[ev.Target]; ok {
			continue
		}
		out = append(out, Violation{
			ConversationID: ev.ConversationID,
			RunID:          ev.RunID,
			Kind:           kind,
			Target:         ev.Target,
			Expected:       expectedRaw,
			Observed:       ev.Target,
			Ts:             ev.Ts,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out
}

// keysCSV returns a sorted comma-separated list of the map's keys.
// Used only for the human-readable Expected field on a Violation — the
// exact format is not part of the wire contract (contract_violations
// view uses a JSON array instead). Kept simple so Verify remains
// obviously pure.
func keysCSV(m map[string]struct{}) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Manual concat — a fmt.Sprintf or strings.Join both pull in
	// zero-cost helpers, but the goal here is to make the "Verify has
	// no external dep" property obvious to a reviewer.
	out := keys[0]
	for _, k := range keys[1:] {
		out += "," + k
	}
	return out
}

// ArtifactHashAppender is the seam through which the audit layer feeds
// the D1 runs.artifact_hashes column. As of 2026-07-01
// internal/evalrun.Writer exposes only whole-row Insert; there is no
// AppendArtifactHashes yet.
//
// TODO(WT-2-metric-extract or D1-completion): replace
// NopArtifactHashAppender with an SQLWriter-backed appender that
// updates runs.artifact_hashes for run_id. Signature MUST be
// Append(ctx, runID string, hashes []string) error. Do NOT add TODO
// comments or wiring code at any file outside internal/journal/ —
// spec §5.1 keeps the seam here.
type ArtifactHashAppender interface {
	// Append idempotently unions hashes into the artifact_hashes JSON
	// array for run_id. Every implementation (including the Nop)
	// MUST reject any hash that does not match ^[a-f0-9]{64}$ with
	// ErrInvalidArtifactHash before doing any other work (spec §7 (d)).
	// Sort order in the persisted array MUST be sha256-hex
	// lexicographic ascending; duplicates MUST be de-duped.
	Append(ctx context.Context, runID string, hashes []string) error
}

// NopArtifactHashAppender is the default appender used until the
// follow-up wiring WT swaps in a real one. It STILL validates every
// hash so §7 (d)'s invariant "no non-hex hash ever reaches an Append
// call successfully" holds even in the noop path.
type NopArtifactHashAppender struct{}

// Append validates then no-ops. Returns ErrInvalidArtifactHash (wrapped
// with the offending index and value) at the first non-hex entry.
func (NopArtifactHashAppender) Append(_ context.Context, _ string, hashes []string) error {
	for i, h := range hashes {
		if !sha256HexRe.MatchString(h) {
			return fmt.Errorf("journal: artifact_hashes[%d]=%q: %w", i, h, ErrInvalidArtifactHash)
		}
	}
	return nil
}

// SortAndDedupeHashes returns a fresh slice of the input hashes with
// duplicates removed and elements in sha256-hex lexicographic
// ascending order. Callers pass the result to
// ArtifactHashAppender.Append. Rejects (returns error) if any hash
// fails the ^[a-f0-9]{64}$ regex.
//
// Exported so the follow-up wiring WT and the audit-layer harness use
// the SAME implementation; the sort+dedupe contract is spelled once.
func SortAndDedupeHashes(hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(hashes))
	for i, h := range hashes {
		if !sha256HexRe.MatchString(h) {
			return nil, fmt.Errorf("journal: SortAndDedupeHashes[%d]=%q: %w", i, h, ErrInvalidArtifactHash)
		}
		seen[h] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// AppendHashesFromEvents is the audit-layer helper that packages the
// (Verify → sort+dedup → Append) sequence spec §2.4 requires. Called
// AFTER Verify returned an empty violation list for the events (spec
// §2.4 "contract-violating writes never contribute to artifact_hashes").
// The caller MUST enforce that precondition — passing events from a
// run with unresolved violations is a wiring bug.
//
// Returns nil if events is empty, or if no event carries a KindWrite +
// non-empty Hash.
func AppendHashesFromEvents(ctx context.Context, a ArtifactHashAppender, runID string, events []AuditEvent) error {
	hashes := make([]string, 0)
	for _, ev := range events {
		if ev.Kind == KindWrite && ev.Hash != "" {
			hashes = append(hashes, ev.Hash)
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	deduped, err := SortAndDedupeHashes(hashes)
	if err != nil {
		return err
	}
	return a.Append(ctx, runID, deduped)
}
