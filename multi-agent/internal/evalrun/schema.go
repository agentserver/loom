// Package evalrun owns the per-run D1 evaluation schema for paper-v3.
// See docs/specs/wt1-run-schema.spec.md for the field-by-field contract
// and the security mitigations (a)–(f) the package implements.
package evalrun

import (
	"errors"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// Schema is one row in the runs table — the per-run D1 record. Field
// order matches /root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md
// lines 256–279 and the DDL in internal/observerstore/schema.sql.
type Schema struct {
	RunID                  string    // 08:256 PK
	WorkloadID             string    // 08:257
	ClaimID                string    // 08:258
	ExperimentID           string    // 08:259
	BaselineOrAblation     string    // 08:260
	LoomCommit             string    // 08:261
	AgentserverCommit      string    // 08:262
	ModelserverCommit      string    // 08:263
	AppCommit              string    // 08:264
	MachineTopology        string    // 08:265
	ContextGroundTruth     string    // 08:266
	CapabilitySnapshotHash string    // 08:267 — interface placeholder
	TaskContractHash       string    // 08:268 — interface placeholder
	DynamicMCPRegistryHash string    // 08:269 — interface placeholder
	SelectedContext        string    // 08:270
	GroundTruthContext     string    // 08:271
	StartTime              time.Time // 08:272 UTC RFC3339Nano on the wire
	EndTime                time.Time // 08:273 UTC RFC3339Nano on the wire
	SuccessOracleResult    string    // 08:274 "pass" | "fail" | "timeout"
	FailureCategory        string    // 08:275 11-class D4 constant; "" iff result=="pass"
	HumanInterventionCount int       // 08:276
	ArtifactHashes         []string  // 08:277 each MUST match ^[a-f0-9]{64}$
	ObserverTracePath      string    // 08:278
	ModelTraceID           string    // 08:279
}

// Sentinel errors returned (wrapped via fmt.Errorf("...: %w", sentinel))
// by (*SQLWriter).Insert and NewSQLWriter on invalid input / drifted
// schema. Callers MUST test with errors.Is — string contents are not
// part of the API contract.
var (
	ErrInvalidRunID          = errors.New("evalrun: invalid run_id format")
	ErrInvalidArtifactHash   = errors.New("evalrun: invalid artifact_hashes entry (must be sha256 hex)")
	ErrTooManyArtifactHashes = errors.New("evalrun: artifact_hashes slice length exceeds cap")
	ErrOversizedField        = errors.New("evalrun: field exceeds 8 KiB limit")
	ErrInvalidOracleResult   = errors.New("evalrun: success_oracle_result must be pass|fail|timeout")
	ErrInvalidTime           = errors.New("evalrun: start_time/end_time must be non-zero")
	ErrFailureCategoryOnPass = errors.New("evalrun: failure_category must be empty when success_oracle_result is pass")
	ErrSchemaDrift           = errors.New("evalrun: runs table schema does not match expected layout")
)

// DisableTelemetry, when true, makes (*SQLWriter).Insert skip the DB
// write AND log one structured line per dropped run. Wired into
// ablation.Default via the init() below as ablation.NoObserver.
//
// Mutation is intended to happen pre-run only (the CLI binder in Phase 2
// WT-2-flag-integration flips it before any Insert starts). Concurrent
// mutation with Insert calls is undefined.
var DisableTelemetry bool

// maxFieldBytes is the hard cap on any single string field (security §7
// (f)). 8 KiB = two orders of magnitude over the longest legitimate
// value; the cap is a DoS-via-large-row backstop.
const maxFieldBytes = 8 * 1024

// maxArtifactHashes caps the ArtifactHashes slice length. Each entry
// is 64 hex bytes (validated), so the encoded JSON tops out at roughly
// 256 * 67 ≈ 17 KiB — still a DoS-resistant ceiling, while large
// enough for any realistic per-run artifact set (oracle outputs,
// captured screenshots, intermediate files). Without this cap, a
// caller could pass len = 10M valid hashes and the 8 KiB per-string
// guarantee for the artifact_hashes column would not hold.
const maxArtifactHashes = 256

// runIDRe enforces ^[A-Za-z0-9_-]{8,128}$ — see spec §2.2.
var runIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// sha256HexRe enforces ^[a-f0-9]{64}$ — see spec §7 (b).
var sha256HexRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// validOracleResults is the closed set for success_oracle_result.
var validOracleResults = map[string]struct{}{
	"pass":    {},
	"fail":    {},
	"timeout": {},
}

// initRegistrationErr is sticky state: set if init()'s
// ablation.Default.Register call failed (e.g. another package already
// registered NoObserver under its own *bool, leaving DisableTelemetry
// inert). Insert reads this on its first call and surfaces a louder
// warning via initWarnOnce — without that, the only signal would be
// the single log.Printf at process startup, which an operator running
// `eval-runner ... 2>/dev/null` would never see, defeating spec §7(c).
// Exposed via InitError() so the CLI binder can fail fast.
var (
	initRegistrationErr error
	initWarnOnce        sync.Once
)

// InitError reports whether the package's init-time registration of
// NoObserver against ablation.Default failed. The CLI binder MUST check
// this before flipping any ablation flag: if non-nil, a SetByName call
// for NoObserver will flip somebody else's *bool, not this package's.
func InitError() error { return initRegistrationErr }

func init() {
	if err := ablation.Default.Register(ablation.NoObserver, &DisableTelemetry); err != nil {
		// Per spec §1.1 + §7 (a) inherited from ablation: NEVER panic in
		// init(). Pin the error so consumers can detect the
		// silently-inert state, then log once now AND again on first
		// Insert (operators frequently miss startup-time log lines).
		initRegistrationErr = err
		log.Printf("evalrun: ablation.Default.Register(NoObserver) failed: %v", err)
	}
}
