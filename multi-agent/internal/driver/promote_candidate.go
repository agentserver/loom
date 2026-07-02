package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/observer"
)

// promote_candidate.go — spec docs/specs/wt2-driver-promotion-chain-B1.spec.md.

// CandidateSignal is the input to SurfacePromoteCandidate.
type CandidateSignal struct {
	Family        string
	SourceTaskIDs []string
	SurfacedBy    string
	WorkspaceID   string
}

// CandidateDecision — spec §2.3.
type CandidateDecision string

const (
	DecisionPending  CandidateDecision = ""
	DecisionPromoted CandidateDecision = "promoted"
	DecisionDeclined CandidateDecision = "declined"
	DecisionExpired  CandidateDecision = "expired"
)

// PromoteCandidatesWriter is the driver-side view of the observer
// writer. Kept as an interface here (structurally identical to
// observerstore.PromoteCandidatesWriter) so internal/driver stays
// free of the observerstore import; the driver-agent wiring adapts
// via a thin closure per §6.
type PromoteCandidatesWriter interface {
	InsertPromoteCandidate(ctx context.Context, row PromoteCandidateRow) error
	UpdatePromoteCandidateDecision(ctx context.Context, candidateID, decision, decisionAt string) error
	ExpirePromoteCandidates(ctx context.Context, cutoffRFC3339 string) (int, error)
}

// PromoteCandidateRow is the driver-side row type. Structurally
// identical to observerstore.PromoteCandidateRow.
type PromoteCandidateRow struct {
	CandidateID   string
	Family        string
	SourceTaskIDs string
	SurfacedAt    string
	SurfacedBy    string
	WorkspaceID   string
	RunID         string
}

// PromoteCandidateDeps is the injected dependency bundle. All nil
// fields degrade with a first-use WARN log.
type PromoteCandidateDeps struct {
	Writer PromoteCandidatesWriter
	Events ObserverSink // driver-local interface with a single Emit
	Now    func() time.Time
}

var (
	promoDepsMu           sync.RWMutex
	promoDeps             PromoteCandidateDeps
	promoWriterWarnOnce   sync.Once
)

// SetPromoteCandidateDeps publishes the dependency bundle. Called
// once at process start.
func SetPromoteCandidateDeps(d PromoteCandidateDeps) {
	promoDepsMu.Lock()
	defer promoDepsMu.Unlock()
	promoDeps = d
}

func getPromoteCandidateDeps() PromoteCandidateDeps {
	promoDepsMu.RLock()
	defer promoDepsMu.RUnlock()
	return promoDeps
}

func resetPromoteCandidateDepsForTest() {
	promoDepsMu.Lock()
	defer promoDepsMu.Unlock()
	promoDeps = PromoteCandidateDeps{}
	promoWriterWarnOnce = sync.Once{}
}

// Validation regexes — spec §7 (a), (b).
var (
	promoteCandFamilyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	promoteCandTaskIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

var (
	ErrEmptySourceTaskIDs      = errors.New("promote_candidate: source_task_ids must be non-empty")
	ErrInvalidFamily           = errors.New("promote_candidate: family must match ^[a-z][a-z0-9_-]{0,63}$")
	ErrInvalidSourceTaskID     = errors.New("promote_candidate: source_task_id must match ^[A-Za-z0-9_-]{8,128}$")
	ErrInvalidSurfacedBy       = errors.New("promote_candidate: surfaced_by must be one of user_hint|driver_inferred|similarity_signal")
	ErrCandidateWriterUnwired  = errors.New("promote_candidate: writer dep unwired")
)

var validSurfacedBy = map[string]bool{
	"user_hint":         true,
	"driver_inferred":   true,
	"similarity_signal": true,
}

// computeCandidateID derives the run-scoped candidate id per spec
// §4.1. Empty runID is accepted; the empty string just becomes part
// of the hash input.
func computeCandidateID(runID, family string, taskIDs []string) string {
	sorted := make([]string, len(taskIDs))
	copy(sorted, taskIDs)
	sort.Strings(sorted)
	h := sha256.New()
	h.Write([]byte(runID))
	h.Write([]byte{0x1f}) // US
	h.Write([]byte(family))
	h.Write([]byte{0x1f})
	for i, t := range sorted {
		if i > 0 {
			h.Write([]byte{0x1f})
		}
		h.Write([]byte(t))
	}
	return "cand_" + hex.EncodeToString(h.Sum(nil))[:24]
}

// SurfacePromoteCandidate — spec §3.
func SurfacePromoteCandidate(ctx context.Context, sig CandidateSignal) (string, error) {
	surfacePromotionInitErrorOnce()

	// Ablation short-circuit BEFORE any other side effect.
	if IsNoUserPromotionPath() {
		log.Printf("[ablation] NoUserPromotionPath: candidate suppressed family=%s", sig.Family)
		return "", nil
	}

	// Validate.
	if !promoteCandFamilyRE.MatchString(sig.Family) {
		return "", fmt.Errorf("promote_candidate: family=%q: %w", sig.Family, ErrInvalidFamily)
	}
	if len(sig.SourceTaskIDs) == 0 {
		return "", ErrEmptySourceTaskIDs
	}
	for i, tid := range sig.SourceTaskIDs {
		if !promoteCandTaskIDRE.MatchString(tid) {
			return "", fmt.Errorf("promote_candidate: source_task_ids[%d]=%q: %w", i, tid, ErrInvalidSourceTaskID)
		}
	}
	if _, ok := validSurfacedBy[sig.SurfacedBy]; !ok {
		return "", fmt.Errorf("promote_candidate: surfaced_by=%q: %w", sig.SurfacedBy, ErrInvalidSurfacedBy)
	}

	runID := CurrentRunID()
	cid := computeCandidateID(runID, sig.Family, sig.SourceTaskIDs)

	deps := getPromoteCandidateDeps()
	now := time.Now().UTC()
	if deps.Now != nil {
		now = deps.Now().UTC()
	}
	surfacedAt := now.Format("2006-01-02T15:04:05.000000000Z07:00")

	taskIDsJSON, err := json.Marshal(sig.SourceTaskIDs)
	if err != nil {
		return "", fmt.Errorf("promote_candidate: marshal source_task_ids: %w", err)
	}

	if deps.Writer == nil {
		promoWriterWarnOnce.Do(func() {
			log.Printf("[warn] PromoteCandidateDeps.Writer unwired — candidate rows not persisted for this process")
		})
	} else {
		if err := deps.Writer.InsertPromoteCandidate(ctx, PromoteCandidateRow{
			CandidateID:   cid,
			Family:        sig.Family,
			SourceTaskIDs: string(taskIDsJSON),
			SurfacedAt:    surfacedAt,
			SurfacedBy:    sig.SurfacedBy,
			WorkspaceID:   sig.WorkspaceID,
			RunID:         runID,
		}); err != nil {
			return "", fmt.Errorf("promote_candidate: insert: %w", err)
		}
	}

	// Emit the observer event. Payload EXCLUDES source_task_ids
	// (which live in the DB row) per spec §4.4.
	if deps.Events != nil {
		payload, _ := json.Marshal(map[string]string{
			"family":       sig.Family,
			"candidate_id": cid,
			"surfaced_by":  sig.SurfacedBy,
		})
		deps.Events.Emit(observer.Event{
			WorkspaceID: sig.WorkspaceID,
			AgentRole:   observer.RoleDriver,
			Type:        "promote_candidate",
			Payload:     payload,
		})
	}
	return cid, nil
}

// RecordCandidateDecision — spec §3.
func RecordCandidateDecision(ctx context.Context, candidateID string, dec CandidateDecision, decisionAt time.Time) error {
	deps := getPromoteCandidateDeps()
	if deps.Writer == nil {
		return ErrCandidateWriterUnwired
	}
	if dec != DecisionPromoted && dec != DecisionDeclined && dec != DecisionExpired {
		return fmt.Errorf("promote_candidate: invalid decision %q", dec)
	}
	at := decisionAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	return deps.Writer.UpdatePromoteCandidateDecision(ctx, candidateID, string(dec), at)
}

// ExpireCandidatesOlderThan — spec §3.
func ExpireCandidatesOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	deps := getPromoteCandidateDeps()
	if deps.Writer == nil {
		return 0, ErrCandidateWriterUnwired
	}
	cutoffStr := cutoff.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	return deps.Writer.ExpirePromoteCandidates(ctx, cutoffStr)
}

// --- Detector (spec §4.2) -------------------------------------------

var (
	familyCountsMu sync.Mutex
	familyCounts   = map[string][]string{} // family → observed task_ids
)

// RecordAdHocScriptTask — spec §4.2. Called by the driver's
// task-completion hook when an ad-hoc bash / powershell task
// completes. Second observation of the same family fires
// SurfacePromoteCandidate.
func RecordAdHocScriptTask(ctx context.Context, family, taskID, workspaceID string) {
	if family == "" {
		return
	}
	// LOOM_EVAL_TASK_FAMILY env override — spec §4.2 last para.
	if env := os.Getenv("LOOM_EVAL_TASK_FAMILY"); env != "" {
		family = env
	}
	// Task IDs shorter than 8 chars fail SurfacePromoteCandidate's
	// regex; skip so we don't accumulate garbage in the map.
	if !promoteCandTaskIDRE.MatchString(taskID) {
		return
	}
	familyCountsMu.Lock()
	prior := familyCounts[family]
	// Dedup by task id — a re-run of the same task shouldn't count twice.
	dup := false
	for _, existing := range prior {
		if existing == taskID {
			dup = true
			break
		}
	}
	if !dup {
		prior = append(prior, taskID)
		familyCounts[family] = prior
	}
	shouldFire := len(prior) >= 2 && !dup
	// Copy the slice so we can release the lock before firing.
	obs := make([]string, len(prior))
	copy(obs, prior)
	familyCountsMu.Unlock()

	if !shouldFire {
		return
	}
	_, err := SurfacePromoteCandidate(ctx, CandidateSignal{
		Family:        family,
		SourceTaskIDs: obs,
		SurfacedBy:    "similarity_signal",
		WorkspaceID:   workspaceID,
	})
	if err != nil {
		log.Printf("promote_candidate: RecordAdHocScriptTask surface err (family=%s): %v", family, err)
	}
}

// FamilyOfTaskSummary is the min-viable heuristic: first
// whitespace-delimited token, lowercased, hyphen-stripped. Empty
// input → empty output (caller ignores).
func FamilyOfTaskSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	tokens := strings.Fields(summary)
	if len(tokens) == 0 {
		return ""
	}
	fam := strings.ToLower(tokens[0])
	// Strip anything outside the family regex character class.
	fam = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return -1
		}
	}, fam)
	if fam == "" || !promoteCandFamilyRE.MatchString(fam) {
		return ""
	}
	return fam
}

// resetFamilyCountsForTest — test-only.
func resetFamilyCountsForTest() {
	familyCountsMu.Lock()
	familyCounts = map[string][]string{}
	familyCountsMu.Unlock()
}
