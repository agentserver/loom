// Package executor extension: WT-2-task-resume executor-side resume
// pipeline. See docs/specs/wt2-task-resume.spec.md §6.2.
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// Step is one WriteTarget's execution unit for ExecutorResume. See
// spec §6.2.
type Step struct {
	Index   int
	Target  contract.WriteTarget
	Payload []byte // nil → §6.2 step 1a nil-payload fallthrough
}

// ResumeRequest bundles the inputs ExecutorResume needs.
type ResumeRequest struct {
	TaskID         string
	RunID          string
	ConversationID string
	Contract       contract.TaskContract
	Steps          []Step
}

// ExecutorDeps declares ExecutorResume's collaborators.
type ExecutorDeps struct {
	Store         observerstore.WriteIDStore
	PayloadStager observerstore.PayloadStager

	// WorkerID identifies this ExecutorResume invocation for lease
	// tracking. MUST be INVOCATION-unique (fresh UUIDv4 per call);
	// see spec §6.2.
	WorkerID string

	// LeaseTTL is passed into every Reserve call. Recommended: 30s
	// interactive, up to 5min batch. Minimum 1s.
	LeaseTTL time.Duration

	// WriteStep performs the atomic side effect for one Step; called
	// only after Reserve returns Fresh or Uncommitted. MUST perform
	// an atomic write (temp file + rename) so a crash leaves the
	// target either untouched or with exactly step.Payload.
	WriteStep func(ctx context.Context, step Step) error

	// FreshExecute handles a Step whose Payload is nil (the "step
	// never staged" case). Implementations must themselves call
	// Stage + Reserve + WriteStep + Commit — the same primitives the
	// non-resume happy path uses. Nil is not allowed; the
	// driver-side caller MUST wire this before invoking
	// ExecutorResume.
	FreshExecute func(ctx context.Context, step Step) error
}

// Sentinel error aliases — re-exports so callers can use
// errors.Is against the executor package without importing
// observerstore. See spec §9.
var (
	ErrPayloadUnavailable  = observerstore.ErrPayloadUnavailable
	ErrConcurrentLease     = observerstore.ErrConcurrentLease
	ErrLeaseLostAfterWrite = observerstore.ErrLeaseLostAfterWrite
)

// ErrInvalidExecutorDeps is returned by ExecutorResume when any
// required ExecutorDeps field is nil / zero.
var ErrInvalidExecutorDeps = errors.New("executor: invalid ExecutorDeps")

// ExecutorResume drives the Stage → Reserve → WriteStep → Commit
// pipeline for each Step in req.Steps in declared order. See spec
// §6.2 for the state machine.
func ExecutorResume(ctx context.Context, deps ExecutorDeps, req ResumeRequest) error {
	if deps.Store == nil || deps.PayloadStager == nil ||
		deps.WriteStep == nil || deps.FreshExecute == nil ||
		deps.WorkerID == "" || deps.LeaseTTL <= 0 {
		return ErrInvalidExecutorDeps
	}
	if req.TaskID == "" || req.RunID == "" || req.ConversationID == "" {
		return ErrInvalidExecutorDeps
	}
	for _, step := range req.Steps {
		if err := executeStep(ctx, deps, req, step); err != nil {
			return err
		}
	}
	return nil
}

func executeStep(ctx context.Context, deps ExecutorDeps, req ResumeRequest, step Step) error {
	stepID := fmt.Sprintf("write-%d-%s", step.Index, step.Target.Name)

	// §6.2 step 1a: nil-payload fallthrough → FreshExecute.
	if step.Payload == nil {
		return deps.FreshExecute(ctx, step)
	}

	// §6.2 step 2: compute intended contentHash.
	sum := sha256.Sum256(step.Payload)
	contentHash := hex.EncodeToString(sum[:])

	// §6.2 step 3: derive WriteID.
	id, err := observerstore.NewWriteID(req.TaskID, req.ConversationID,
		stepID, step.Target.Name, contentHash)
	if err != nil {
		return fmt.Errorf("executor: derive WriteID for step %d: %w", step.Index, err)
	}

	// §6.2 step 4: Load first — skip re-Stage if byte-identical row
	// already exists.
	if _, err := deps.PayloadStager.Load(ctx, id); err != nil {
		if !errors.Is(err, ErrPayloadUnavailable) {
			return fmt.Errorf("executor: load payload for step %d: %w", step.Index, err)
		}
		// §6.2 step 5: Stage fresh.
		if err := deps.PayloadStager.Stage(ctx, id, req.TaskID, stepID, step.Payload); err != nil {
			return fmt.Errorf("executor: stage payload for step %d: %w", step.Index, err)
		}
	}

	// §6.2 step 6: Reserve.
	state, err := deps.Store.Reserve(ctx, observerstore.ReserveRequest{
		ID:             id,
		RunID:          req.RunID,
		TaskID:         req.TaskID,
		ConversationID: req.ConversationID,
		StepID:         stepID,
		WorkerID:       deps.WorkerID,
		LeaseTTL:       deps.LeaseTTL,
	})
	if err != nil {
		return fmt.Errorf("executor: reserve step %d: %w", step.Index, err)
	}

	switch state {
	case observerstore.ReserveFresh, observerstore.ReserveUncommitted:
		if err := deps.WriteStep(ctx, step); err != nil {
			return fmt.Errorf("executor: write step %d: %w", step.Index, err)
		}
		if err := deps.Store.Commit(ctx, observerstore.CommitRequest{
			ID:       id,
			RunID:    req.RunID,
			TaskID:   req.TaskID,
			WorkerID: deps.WorkerID,
		}); err != nil {
			return fmt.Errorf("executor: commit step %d: %w", step.Index, err)
		}
	case observerstore.ReserveInFlight:
		return fmt.Errorf("executor: step %d: %w", step.Index, ErrConcurrentLease)
	case observerstore.ReserveCommitted:
		// Skip; prior attempt durably completed.
		return nil
	default:
		return fmt.Errorf("executor: unknown reserve state %v for step %d", state, step.Index)
	}
	return nil
}
