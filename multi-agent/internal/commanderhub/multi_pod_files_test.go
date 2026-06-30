package commanderhub

// multi_pod_files_test.go — integration tests for the read_file capability gate
// across two in-process pods sharing a Postgres database.
//
// Env-gated: set OBSERVER_POSTGRES_TEST_DSN to run these tests.
// Without the DSN they t.Skip immediately (via requirePG in multi_pod_test.go).

import (
	"context"
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
// the forward succeeds and returns a result that does not exceed 768 KiB.
func TestMultiPod_ReadFile_ForwardedFromB_RespectsCapInA(t *testing.T) {
	db := requirePG(t)
	migrateAll(t, db)
	cleanupTables(t, db)

	secret := []byte("shared-secret-12")
	podA := newFakePod(t, db, "pod-a", "http://pod-a.internal", secret, nil)
	podB := newFakePod(t, db, "pod-b", "http://pod-b.internal", secret, nil)

	// Pod A holds a modern daemon with file_preview_encoded_cap.
	dcA := addLocalDaemon(t, podA, "modern-daemon", commander.CapabilityFilePreviewEncodedCap)

	// Small base64-encoded file content well under the 768 KiB cap.
	const maxReadFileBytes = 768 * 1024
	fakeFileContent := []byte(`{"content":"aGVsbG8gd29ybGQ=","encoding":"base64","truncated":false}`)

	// Daemon goroutine: wait for a pending entry from the forwarded read_file
	// command, then route back a command_result.
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
			Payload: fakeFileContent,
		})
	}()

	ctx := context.Background()

	// Pod B calls ReadFile — this forward to pod A which succeeds (cap present).
	result, err := podB.hub.ReadFile(ctx, multiPodOwner, "modern-daemon", "sess-1", "/hello.txt")

	// Wait for daemon goroutine (cleanup).
	<-daemonDone

	require.NoError(t, err, "ReadFile on modern daemon must succeed")
	require.NotNil(t, result, "result must be non-nil")
	require.LessOrEqual(t, len(result), maxReadFileBytes,
		"ReadFile result must not exceed 768 KiB")
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
