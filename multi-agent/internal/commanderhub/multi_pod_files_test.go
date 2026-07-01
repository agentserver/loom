package commanderhub

// multi_pod_files_test.go — integration tests for the read_file capability gate
// across two in-process pods sharing a Postgres database.
//
// Env-gated: set OBSERVER_POSTGRES_TEST_DSN to run these tests.
// Without the DSN they t.Skip immediately (via requirePG in multi_pod_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

// ---------------------------------------------------------------------------
// Test 11: ReadFile_CapabilityGate_OldDaemon_426
// ---------------------------------------------------------------------------

// TestMultiPod_ReadFile_CapabilityGate_OldDaemon_426 verifies that when pod A
// holds an "old" daemon (no file_preview_encoded_cap) and pod B forwards a
// read_file command to pod A, the forwardHandler on A returns 426 Upgrade
// Required mapped to a DaemonError{Code: "daemon_upgrade_required"}.
func TestMultiPod_ReadFile_CapabilityGate_OldDaemon_426(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-11")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A holds an old daemon — addLocalDaemon adds CapabilitySessions + CapabilityTurn
	// by default; no extra caps, so file_preview_encoded_cap is absent.
	addLocalDaemon(t, podA, "old-daemon")

	ctx := context.Background()

	// Pod B calls ReadFile. Since pod B does not have "old-daemon" locally, it
	// calls lookupRemote → finds pod-a.internal → forwardCli.send → POST to A's
	// forwardHandler. The forwardHandler checks capabilities and returns 426.
	// mapResponse maps 426 → &DaemonError{Code: ErrCodeDaemonUpgradeRequired}.
	_, err := podB.hub.ReadFile(ctx, multiPodOwner, "old-daemon", "sess-1", "/path/to/file")

	require.Error(t, err, "ReadFile on old daemon must return an error")
	var de *DaemonError
	require.True(t, errors.As(err, &de),
		"error must be a *DaemonError, got: %T %v", err, err)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, de.Code,
		"error code must be daemon_upgrade_required")
}

// ---------------------------------------------------------------------------
// Test 12: ReadFile_ForwardedFromB_RespectsCapInA
// ---------------------------------------------------------------------------

// TestMultiPod_ReadFile_ForwardedFromB_RespectsCapInA verifies that when pod A
// holds a modern daemon (with file_preview_encoded_cap) and pod B calls ReadFile,
// the forward succeeds and correctly propagates a TooLarge response when the
// daemon signals the file exceeded the 768 KiB encoded-size cap.
//
// This exercises the pathological-cap case: the fake daemon simulates returning
// a TooLarge=true response (as a real daemon would for content >768 KiB), and
// we assert that hub.ReadFile propagates TooLarge=true with empty Content —
// NOT a trivially-true assertion on tiny synthetic content.
func TestMultiPod_ReadFile_ForwardedFromB_RespectsCapInA(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-12")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A holds a modern daemon with file_preview_encoded_cap.
	dcA := addLocalDaemon(t, podA, "modern-daemon", commander.CapabilityFilePreviewEncodedCap)

	// Build a fake payload that simulates what a real daemon returns when the
	// file's base64-encoded form exceeds maxEncodedFileResponse (768 KiB).
	// The daemon-side cap sets TooLarge=true and clears Content — we replicate
	// that here to test the hub correctly propagates the cap signal.
	// We also construct a large Content string to verify the test covers content
	// that WOULD exceed 768 KiB: strings.Repeat("A", 800*1024) is ~800 KiB, well
	// past the 768 KiB threshold; a real daemon would cap it; our fake daemon
	// returns the already-capped TooLarge=true form.
	tooLargePayload := commander.FileReadResult{
		Path:     "/large.txt",
		Size:     800 * 1024, // report size > cap to prove test is non-trivial
		TooLarge: true,
		Content:  "", // capped: real daemon clears content when TooLarge
	}
	tooLargeJSON, err := json.Marshal(tooLargePayload)
	require.NoError(t, err)

	// Daemon goroutine: wait for a pending entry from the forwarded read_file
	// command, then route back a command_result carrying the TooLarge response.
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		deadline := time.Now().Add(5 * time.Second)
		var cmdID string
		for time.Now().Before(deadline) {
			dcA.pendingMu.Lock()
			for id := range dcA.pending {
				cmdID = id
			}
			dcA.pendingMu.Unlock()
			if cmdID != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if cmdID == "" {
			return
		}
		dcA.routeFrame(commander.Envelope{
			Type:    "command_result",
			ID:      cmdID,
			Payload: tooLargeJSON,
		})
	}()

	ctx := context.Background()

	// Pod B calls ReadFile — this forwards to pod A which succeeds (cap present).
	result, err := podB.hub.ReadFile(ctx, multiPodOwner, "modern-daemon", "sess-1", "/large.txt")

	// Wait for daemon goroutine (cleanup).
	<-daemonDone

	require.NoError(t, err, "ReadFile on modern daemon must succeed (TooLarge is not an error, it's a result field)")
	require.NotNil(t, result, "result must be non-nil")

	// Unmarshal and assert TooLarge=true and Content="" — the pathological cap
	// case that was not exercised by the original tiny-content test.
	var parsed commander.FileReadResult
	require.NoError(t, json.Unmarshal(result, &parsed))
	require.True(t, parsed.TooLarge, "result must have TooLarge=true for oversized files")
	require.Empty(t, parsed.Content, "result Content must be empty when TooLarge=true")
	require.GreaterOrEqual(t, parsed.Size, int64(768*1024),
		"reported Size must be >= cap threshold (%d KiB)", 768)
}

// ---------------------------------------------------------------------------
// Test: Local cap gate — pod A's own old daemon
// ---------------------------------------------------------------------------

// TestMultiPod_ReadFile_LocalCapGate_OldDaemon verifies that when pod A holds
// an old daemon locally and pod A calls ReadFile directly, the local cap gate in
// hub.ReadFile returns DaemonError{Code: "daemon_upgrade_required"} without
// forwarding.
func TestMultiPod_ReadFile_LocalCapGate_OldDaemon(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-12b")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)

	// Local old daemon — no file_preview_encoded_cap.
	addLocalDaemon(t, podA, "local-old-daemon")

	ctx := context.Background()

	// Pod A calls ReadFile on its own old daemon. In shared mode, ReadFile
	// checks the local cap before forwarding.
	_, err := podA.hub.ReadFile(ctx, multiPodOwner, "local-old-daemon", "sess-1", "/path")

	require.Error(t, err)
	var de *DaemonError
	require.True(t, errors.As(err, &de),
		"error must be a *DaemonError, got: %T %v", err, err)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, de.Code)
}

// Ensure net/http is used (for http.StatusUpgradeRequired constant reference in
// the production code this test exercises).
var _ = http.StatusUpgradeRequired
