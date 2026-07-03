package observerstore

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchema_RegistryLookupSamplesExists(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	cols := tableColumns(t, s, "registry_lookup_samples")
	for _, want := range []string{
		"row_id", "ts", "run_id", "workspace_id", "query_hash_prefix",
		"hit_count", "registry_hits", "userspace_hits", "top_score",
	} {
		if !cols[want] {
			t.Errorf("registry_lookup_samples missing column %q", want)
		}
	}
}

func TestRegistryLookupSamplesWriter_RoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewRegistryLookupSamplesWriter(s.DB())

	row := RegistryLookupSampleRow{
		TS:              time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		RunID:           "run-abc12345",
		WorkspaceID:     "ws-abc12345",
		QueryHashPrefix: "abcd1234",
		HitCount:        3,
		RegistryHits:    2,
		UserspaceHits:   1,
		TopScore:        0.85,
	}
	require.NoError(t, w.WriteRegistryLookupSample(context.Background(), row))

	var got struct {
		rowID, ts, runID, ws, hash string
		hit, reg, us               int
		top                        float64
	}
	err = s.DB().QueryRow(`SELECT row_id, ts, run_id, workspace_id, query_hash_prefix,
	    hit_count, registry_hits, userspace_hits, top_score FROM registry_lookup_samples`).Scan(
		&got.rowID, &got.ts, &got.runID, &got.ws, &got.hash,
		&got.hit, &got.reg, &got.us, &got.top,
	)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(got.rowID, "regslot_"))
	require.Equal(t, "run-abc12345", got.runID)
	require.Equal(t, "ws-abc12345", got.ws)
	require.Equal(t, "abcd1234", got.hash)
	require.Equal(t, 3, got.hit)
	require.Equal(t, 2, got.reg)
	require.Equal(t, 1, got.us)
	require.InDelta(t, 0.85, got.top, 0.001)
}

// TestRegistryLookupSamplesWriter_NoObserverAblation — PR #71
// round-2 review P1-B. When the injected `disabled` predicate returns
// true, Write skips the SQL exec and logs
// `[ablation] NoObserver: dropped registry_lookup_samples ...`.
func TestRegistryLookupSamplesWriter_NoObserverAblation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	var disabled bool
	w := NewRegistryLookupSamplesWriterWithAblation(s.DB(), func() bool { return disabled })

	var buf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(prevFlags) }()

	// Ablation OFF — row lands.
	require.NoError(t, w.WriteRegistryLookupSample(context.Background(), RegistryLookupSampleRow{
		TS:              time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		RunID:           "run-a",
		QueryHashPrefix: "abcd1234",
	}))
	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM registry_lookup_samples`).Scan(&n))
	require.Equal(t, 1, n)

	// Ablation ON — subsequent Write drops with log line.
	disabled = true
	buf.Reset()
	require.NoError(t, w.WriteRegistryLookupSample(context.Background(), RegistryLookupSampleRow{
		TS:              time.Date(2026, 7, 2, 13, 0, 0, 0, time.UTC),
		RunID:           "run-b",
		QueryHashPrefix: "beef1234",
	}))
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM registry_lookup_samples`).Scan(&n))
	require.Equal(t, 1, n, "ablated Write must NOT persist")
	require.Contains(t, buf.String(), "[ablation] NoObserver: dropped registry_lookup_samples row_id=regslot_")
	require.Contains(t, buf.String(), "run_id=run-b")
	require.Contains(t, buf.String(), "query_hash=beef1234")
}

func TestRegistryLookupSamplesWriter_ParameterizedSQL_NoInjection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	w := NewRegistryLookupSamplesWriter(s.DB())

	// Feed a malicious payload through a field the caller *shouldn't*
	// put non-hex into (query_hash_prefix); the writer's `?` binding
	// preserves it verbatim.
	malicious := `'); DROP TABLE registry_lookup_samples; --`
	require.NoError(t, w.WriteRegistryLookupSample(context.Background(), RegistryLookupSampleRow{
		TS:              time.Now().UTC(),
		QueryHashPrefix: malicious,
	}))
	// Table still exists.
	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM registry_lookup_samples`).Scan(&n))
	require.Equal(t, 1, n)
}
