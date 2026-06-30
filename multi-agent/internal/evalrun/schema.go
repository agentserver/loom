// Package evalrun owns the per-run D1 evaluation schema for paper-v3.
// See docs/specs/wt1-run-schema.spec.md for the field-by-field contract
// and the security mitigations (a)–(f) the package implements.
package evalrun

import (
	"errors"
	"log"
	"regexp"
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
	ErrInvalidRunID        = errors.New("evalrun: invalid run_id format")
	ErrInvalidArtifactHash = errors.New("evalrun: invalid artifact_hashes entry (must be sha256 hex)")
	ErrOversizedField      = errors.New("evalrun: field exceeds 8 KiB limit")
	ErrInvalidOracleResult = errors.New("evalrun: success_oracle_result must be pass|fail|timeout")
	ErrInvalidTime         = errors.New("evalrun: start_time/end_time must be non-zero")
	ErrSchemaDrift         = errors.New("evalrun: runs table schema does not match expected 24-column descriptor")
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

func init() {
	if err := ablation.Default.Register(ablation.NoObserver, &DisableTelemetry); err != nil {
		// Per spec §1.1 + §7 (a) inherited from ablation: NEVER panic in
		// init(). Log and continue; SetByName will return ErrNotRegistered
		// for NoObserver downstream, which is the correct diagnostic.
		log.Printf("evalrun: ablation.Default.Register(NoObserver) failed: %v", err)
	}
}
