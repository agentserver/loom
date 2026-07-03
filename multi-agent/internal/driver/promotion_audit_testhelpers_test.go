package driver

import (
	"context"
	"errors"

	"github.com/yourorg/multi-agent/internal/promotionaudit"
)

// recordingWriter is a promotionaudit.Writer that captures rows in
// memory for test inspection.
type recordingWriter struct {
	rows []promotionaudit.AuditFields
	// writeErr is unused; kept for source compatibility with older
	// test files that expected an io.Reader-shaped error hook.
	writeErr interface{}
}

func (r *recordingWriter) Write(_ context.Context, f promotionaudit.AuditFields) error {
	r.rows = append(r.rows, f)
	return nil
}

func (r *recordingWriter) Close() error { return nil }

// errorWriter always returns an error on Write. Used to test the
// audit-failure-degrades path (§3.4 step 6).
type errorWriter struct{}

func (errorWriter) Write(_ context.Context, _ promotionaudit.AuditFields) error {
	return errors.New("simulated audit-write failure")
}

func (errorWriter) Close() error { return nil }
