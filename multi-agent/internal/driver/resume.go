// Package driver extension: WT-2-task-resume driver-side ResumeTask
// + ReconstructSteps helper. See docs/specs/wt2-task-resume.spec.md
// §6.1 / §6.2a.
package driver

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// taskIDPattern enforces the §7(d) task_id regex. Compiled once at
// init so a later edit cannot loosen it accidentally without
// breaking tests.
var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// -----------------------------------------------------------------
// Sentinel errors (spec §9)
// -----------------------------------------------------------------

// ErrInvalidTaskID is returned by ResumeTask when taskID fails
// ^[A-Za-z0-9_-]{8,128}$. Aliases observerstore.ErrInvalidTaskIDForm
// so errors.Is works from either package.
var ErrInvalidTaskID = observerstore.ErrInvalidTaskIDForm

// ErrJournalChainBroken re-exports the observerstore sentinel.
// Retained for future per-record chain hardening; not raised by
// this worktree's ResumeTask (§7(e)).
var ErrJournalChainBroken = observerstore.ErrJournalChainBroken

// ErrInvalidResumeDeps is returned when any required ResumeDeps
// field is nil.
var ErrInvalidResumeDeps = errors.New("driver: invalid ResumeDeps")

// ErrContractNotFound is returned by ResumeTask when LoadContract
// reports no row for taskID (spec §6.1b).
var ErrContractNotFound = errors.New("driver: contract not found")

// ErrStepPayloadUnrecoverable is returned by ReconstructSteps when a
// step has no prior staged rows AND the caller supplied no
// fallback bytes. Distinct from ErrPayloadUnavailable (which is a
// "not staged yet" signal that ExecutorResume handles as a
// nil-payload fallthrough).
var ErrStepPayloadUnrecoverable = errors.New("driver: step payload unrecoverable")

// -----------------------------------------------------------------
// ResumeDeps + ResumeTask (§6.1)
// -----------------------------------------------------------------

// ResumeDeps bundles the collaborators ResumeTask needs. Every
// field required; nil returns ErrInvalidResumeDeps. Notably, no
// TaskJournal field — ResumeTask does not read the journal.
type ResumeDeps struct {
	Store        observerstore.WriteIDStore
	LoadContract func(ctx context.Context, taskID string) (contract.TaskContract, error)
	Dispatch     func(ctx context.Context, runID, taskID string, contract contract.TaskContract) error
}

// ResumeTask validates taskID, audits the attempt, loads the
// authoritative contract from the observer DB, and asks the
// dispatcher to re-drive the task. It performs NO journal I/O.
func ResumeTask(ctx context.Context, deps ResumeDeps, runID, taskID string) error {
	if deps.Store == nil || deps.LoadContract == nil || deps.Dispatch == nil {
		return ErrInvalidResumeDeps
	}
	if runID == "" {
		return ErrInvalidResumeDeps
	}
	if !taskIDPattern.MatchString(taskID) {
		return ErrInvalidTaskID
	}

	// Step 2: pre-dispatch audit row.
	if err := deps.Store.RecordResumeAttempt(ctx, runID, taskID, "started", ""); err != nil {
		return fmt.Errorf("driver: record started: %w", err)
	}

	// Step 3: load authoritative contract.
	body, err := deps.LoadContract(ctx, taskID)
	if err != nil {
		errKind := classifyErr(err)
		if auditErr := deps.Store.RecordResumeAttempt(ctx, runID, taskID, "error", errKind); auditErr != nil {
			return errors.Join(err, fmt.Errorf("driver: record error audit: %w", auditErr))
		}
		return err
	}

	// Step 4: dispatch.
	if err := deps.Dispatch(ctx, runID, taskID, body); err != nil {
		errKind := classifyErr(err)
		if auditErr := deps.Store.RecordResumeAttempt(ctx, runID, taskID, "error", errKind); auditErr != nil {
			return errors.Join(err, fmt.Errorf("driver: record error audit: %w", auditErr))
		}
		return err
	}

	// Step 5: replayed.
	if err := deps.Store.RecordResumeAttempt(ctx, runID, taskID, "replayed", ""); err != nil {
		return fmt.Errorf("driver: record replayed: %w", err)
	}
	return nil
}

// classifyErr maps typed errors to the audit `error_kind` label.
// Uses errors.Is so wrapped errors classify correctly.
func classifyErr(err error) string {
	switch {
	case errors.Is(err, ErrContractNotFound):
		return "ErrContractNotFound"
	case errors.Is(err, ErrJournalChainBroken):
		return "ErrJournalChainBroken"
	case errors.Is(err, executor.ErrConcurrentLease):
		return "ErrConcurrentLease"
	case errors.Is(err, executor.ErrPayloadUnavailable):
		return "ErrPayloadUnavailable"
	default:
		return "unknown"
	}
}

// -----------------------------------------------------------------
// ReconstructSteps (§6.2a)
// -----------------------------------------------------------------

// ReconstructSteps assembles []executor.Step for a resumed task by
// consulting the PayloadStager for previously staged bytes.
// Committed-wins policy (spec §6.2a step 2): a committed prior
// attempt anchors the step's Payload to the committed bytes, so
// even a fresh model regeneration cannot rewrite that step.
func ReconstructSteps(ctx context.Context, stager observerstore.PayloadStager,
	taskID string, c contract.TaskContract) ([]executor.Step, error) {
	steps := make([]executor.Step, 0, len(c.DataContract.WriteTargets))
	for i, target := range c.DataContract.WriteTargets {
		stepID := fmt.Sprintf("write-%d-%s", i, target.Name)
		refs, err := stager.ListForStep(ctx, taskID, stepID)
		if err != nil {
			return nil, fmt.Errorf("driver: list step %d: %w", i, err)
		}
		step := executor.Step{Index: i, Target: target}
		switch {
		case len(refs) == 0:
			// No prior stage — Step.Payload stays nil; ExecutorResume
			// takes the FreshExecute fallthrough path.
		default:
			// Priority: latest COMMITTED ref (spec §6.2a step 2) →
			// latest uncommitted (§6.2a step 3) → oldest committed
			// (should be unreachable given the sort order).
			var chosen *observerstore.PayloadRef
			for i := range refs {
				if refs[i].CommittedAt != nil {
					// refs are staged_at DESC; the first committed we
					// hit is the most recent committed.
					chosen = &refs[i]
					break
				}
			}
			if chosen == nil {
				// No committed — use most recent uncommitted (index 0).
				chosen = &refs[0]
			}
			payload, err := stager.Load(ctx, chosen.ID)
			if err != nil {
				// The chosen ref was listed by ListForStep but Load
				// couldn't retrieve it — the row was deleted between
				// the two calls (e.g. by a concurrent Vacuum).
				// Callers can errors.Is-check the sentinel to
				// distinguish this from a generic DB error.
				return nil, fmt.Errorf("driver: load step %d: %w: %w",
					i, ErrStepPayloadUnrecoverable, err)
			}
			step.Payload = payload
		}
		steps = append(steps, step)
	}
	return steps, nil
}
