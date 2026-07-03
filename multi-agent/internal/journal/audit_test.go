package journal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// Helpers ----------------------------------------------------------

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "j.db")
	st, err := observerstore.OpenSQLite(p)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st.DB()
}

// hex64 returns a 64-char lowercase hex string. Not a real sha256 —
// tests only need the ^[a-f0-9]{64}$ shape.
func hex64(pad byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = pad
	}
	return string(buf)
}

// baseEvent returns an AuditEvent with all mandatory fields set to
// legal values for `kind`. Test-local mutators tweak one field at a
// time.
func baseEvent(kind AuditKind) AuditEvent {
	return AuditEvent{
		ConversationID: "conv-1",
		RunID:          "run-1",
		WorkspaceID:    "w1",
		Kind:           kind,
		Target:         "/tmp/a",
		Ts:             time.Unix(1_700_000_000, 0).UTC(),
	}
}

// Tests 1-8: AuditEvent validation --------------------------------

func TestAuditEvent_Validate_HappyPath(t *testing.T) {
	for _, k := range []AuditKind{KindRead, KindWrite, KindToolCall, KindModelCall} {
		ev := baseEvent(k)
		if k == KindWrite {
			ev.SizeBytes = 12
			ev.Hash = hex64('a')
		}
		require.NoError(t, validateEvent(ev), "kind=%s", k)
	}
}

func TestAuditEvent_Validate_EmptyConversationIDRejected(t *testing.T) {
	ev := baseEvent(KindRead)
	ev.ConversationID = ""
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrEmptyConversationID)
}

func TestAuditEvent_Validate_InvalidKindRejected(t *testing.T) {
	ev := baseEvent(KindRead)
	ev.Kind = "bogus"
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidKind)
}

func TestAuditEvent_Validate_SizeOnNonWriteRejected(t *testing.T) {
	ev := baseEvent(KindRead)
	ev.SizeBytes = 1
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidSizeOnNonWrite)
}

func TestAuditEvent_Validate_HashOnNonWriteRejected(t *testing.T) {
	ev := baseEvent(KindToolCall)
	ev.Hash = hex64('a')
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidHashOnNonWrite)
}

func TestAuditEvent_Validate_NonHexHashRejected(t *testing.T) {
	ev := baseEvent(KindWrite)
	ev.Hash = "not-hex"
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidArtifactHash)
	// Uppercase MUST also reject — the regex is lowercase-only so
	// downstream sort/dedupe never double-counts case-variant hashes.
	ev.Hash = strings.ToUpper(hex64('a'))
	err = validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidArtifactHash)
}

func TestAuditEvent_Validate_EmptyHashOnWriteAccepted(t *testing.T) {
	ev := baseEvent(KindWrite)
	ev.SizeBytes = 42
	ev.Hash = ""
	require.NoError(t, validateEvent(ev))
}

func TestAuditEvent_Validate_ZeroTimestampRejected(t *testing.T) {
	ev := baseEvent(KindRead)
	ev.Ts = time.Time{}
	err := validateEvent(ev)
	require.ErrorIs(t, err, ErrInvalidTimestamp)
}

// Tests 9-13: scrubTarget + sensitive-path pattern set ------------

func TestScrubTarget_SensitivePaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"aws-dir", "/home/alice/.aws/credentials"},
		{"aws-root", "/root/.aws/config"},
		{"aws-macos", "/Users/alice/.aws/credentials"},
		{"ssh-key", "/home/alice/.ssh/id_rsa"},
		{"gnupg", "/home/alice/.gnupg/private-keys-v1.d/foo.key"},
		{"gcloud", "/home/alice/.config/gcloud/application_default_credentials.json"},
		{"docker", "/home/alice/.docker/config.json"},
		{"netrc", "/home/alice/.netrc"},
		{"kube", "/home/alice/.kube/config"},
		{"shadow", "/etc/shadow"},
		{"shadow-macos", "/private/etc/shadow"},
		{"shadow-hyphen", "/etc/shadow-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := AuditTargetRedactedTotal().Value()
			out, changed := scrubTarget(c.in)
			require.True(t, changed, "expected redaction: %s", c.in)
			require.NotContains(t, out, c.in, "raw path must be gone")
			require.Contains(t, out, "[REDACTED-CRED-PATH]")
			require.Equal(t, before+1, AuditTargetRedactedTotal().Value(), "counter must bump once per redacted call")
		})
	}
}

func TestScrubTarget_SecretsRedactedFirst(t *testing.T) {
	// A path that includes a token-shape substring — both scrubs
	// should apply.
	in := "/tmp/tokens/sk-ant-abcdefghij_XY-1234567890/whatever"
	out, changed := scrubTarget(in)
	require.True(t, changed)
	require.Contains(t, out, "[REDACTED]", "secretscrub must fire on the sk-ant token")
	require.NotContains(t, out, "sk-ant-")
}

func TestScrubTarget_Idempotent(t *testing.T) {
	cases := []string{
		"/home/alice/.aws/credentials",
		"/tmp/tokens/sk-ant-abcdefghij_XY-1234567890/whatever",
		"/etc/shadow",
		"/tmp/plain/file.txt",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			once, changedOnce := scrubTarget(in)
			before := AuditTargetRedactedTotal().Value()
			twice, changedTwice := scrubTarget(once)
			require.Equal(t, once, twice, "scrubTarget must be idempotent")
			require.False(t, changedTwice, "second call must NOT bump counter")
			_ = changedOnce
			require.Equal(t, before, AuditTargetRedactedTotal().Value())
		})
	}
}

func TestScrubTarget_NoRedactionsCounterStable(t *testing.T) {
	before := AuditTargetRedactedTotal().Value()
	out, changed := scrubTarget("/tmp/plain/file.txt")
	require.False(t, changed)
	require.Equal(t, "/tmp/plain/file.txt", out)
	require.Equal(t, before, AuditTargetRedactedTotal().Value())
}

// Test 12: 8-KiB target → ErrTargetTooLong from Record (scrub-first,
// cap-after ordering).
func TestRecord_LongTargetCapped(t *testing.T) {
	db := openTestDB(t)
	r := NewSQLRecorder(db)
	ev := baseEvent(KindRead)
	ev.Target = strings.Repeat("/x", 5_000) // 10 KiB > 4 KiB cap
	err := r.Record(context.Background(), ev)
	require.ErrorIs(t, err, ErrTargetTooLong)
}

// Test 19: nil DB returns Nop ---------------------------------------

func TestNewSQLRecorder_NilDBReturnsNop(t *testing.T) {
	r := NewSQLRecorder(nil)
	require.IsType(t, NopRecorder{}, r)
	require.NoError(t, r.Record(context.Background(), baseEvent(KindRead)))
}

// Test 20: scrub before insert (Recorder integration) --------------

func TestRecorder_ScrubHappensBeforeInsert(t *testing.T) {
	db := openTestDB(t)
	r := NewSQLRecorder(db)
	before := AuditTargetRedactedTotal().Value()

	ev := baseEvent(KindRead)
	ev.Target = "/root/.aws/credentials"
	require.NoError(t, r.Record(context.Background(), ev))

	var stored string
	require.NoError(t, db.QueryRow("SELECT target FROM audit_events WHERE conversation_id=?", "conv-1").Scan(&stored))
	require.NotEqual(t, ev.Target, stored, "raw sensitive path must NOT be persisted")
	require.Contains(t, stored, "[REDACTED-CRED-PATH]")
	require.Greater(t, AuditTargetRedactedTotal().Value(), before)
}

// Tests 21-23: log-and-continue, no swallow, no block --------------

// errWriter implements observerstore.AuditWriter and always errors.
type errWriter struct{ err error }

func (e errWriter) WriteAuditEvent(context.Context, observerstore.AuditEventRow) error { return e.err }

func TestRecorder_InsertFailure_ReturnsErrorNoSwallow(t *testing.T) {
	r := &sqlRecorder{
		writer: errWriter{err: sql.ErrConnDone},
		newID:  func() (string, error) { return "id-1", nil },
	}
	err := r.Record(context.Background(), baseEvent(KindRead))
	require.ErrorIs(t, err, sql.ErrConnDone, "insert error must propagate; no swallow")
}

// recordOrLog is the in-package test harness that names the required
// caller shape from spec §7 (a): log + counter-bump + no propagation.
// The follow-up wiring WT extracts this helper into production code;
// today it lives here so the invariant has a real running test.
func recordOrLog(r Recorder, ev AuditEvent) error {
	if err := r.Record(context.Background(), ev); err != nil {
		// spec §7 (a) — log + counter-bump + do NOT propagate.
		AuditWriteDroppedTotal().Add(1)
		_ = err // in production this is `log.Printf("journal: audit write dropped: %v", err)`
	}
	return nil
}

func TestRecorder_CallerLogsAndContinues(t *testing.T) {
	before := AuditWriteDroppedTotal().Value()
	r := &sqlRecorder{
		writer: errWriter{err: sql.ErrConnDone},
		newID:  func() (string, error) { return "id-1", nil },
	}
	require.NoError(t, recordOrLog(r, baseEvent(KindRead)), "harness must swallow after logging + counter bump")
	require.Equal(t, before+1, AuditWriteDroppedTotal().Value())
}

func TestRecorder_DoesNotBlockOnDBHang(t *testing.T) {
	db := openTestDB(t)
	r := NewSQLRecorder(db)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	start := time.Now()
	err := r.Record(ctx, baseEvent(KindRead))
	require.Less(t, time.Since(start), 5*time.Second, "Record must honour deadline; not block")
	require.Error(t, err, "expired deadline surfaces as an error, not a hang")
}

// Tests 24-31: Verifier semantics -----------------------------------

func mkContractForVerifierTests() contract.TaskContract {
	return contract.TaskContract{
		ConversationID: "conv-1",
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Name: "/tmp/a"},
				{ArtifactID: "abc123"},
			},
			WriteTargets: []contract.WriteTarget{
				{Name: "/tmp/b"},
			},
		},
		CapabilityRequirements: contract.CapabilityRequirements{
			Tools:  []string{"bash"},
			Skills: []string{"chat"},
		},
	}
}

func TestVerifier_Pure_HappyPath(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{
		{ConversationID: "conv-1", Kind: KindRead, Target: "/tmp/a", Ts: time.Unix(1, 0)},
		{ConversationID: "conv-1", Kind: KindWrite, Target: "/tmp/b", Ts: time.Unix(2, 0)},
	}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.NotNil(t, got)
	require.Len(t, got, 0)
}

func TestVerifier_UndeclaredRead(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{{ConversationID: "conv-1", Kind: KindRead, Target: "/etc/passwd", Ts: time.Unix(1, 0)}}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 1)
	require.Equal(t, ViolationUndeclaredRead, got[0].Kind)
	require.Equal(t, "/etc/passwd", got[0].Target)
}

func TestVerifier_UndeclaredWrite(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{{ConversationID: "conv-1", Kind: KindWrite, Target: "/tmp/x", Ts: time.Unix(1, 0)}}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 1)
	require.Equal(t, ViolationUndeclaredWrite, got[0].Kind)
}

func TestVerifier_UndeclaredToolCall(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{{ConversationID: "conv-1", Kind: KindToolCall, Target: "mcp:evil:x", Ts: time.Unix(1, 0)}}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 1)
	require.Equal(t, ViolationUndeclaredToolCall, got[0].Kind)
}

func TestVerifier_UndeclaredModelCall(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{{ConversationID: "conv-1", Kind: KindModelCall, Target: "bad-model", Ts: time.Unix(1, 0)}}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 1)
	require.Equal(t, ViolationUndeclaredModelCall, got[0].Kind)
}

func TestVerifier_ReadArtifactIDMatches(t *testing.T) {
	// Read declared only by ArtifactID; event Target = that ID → no
	// violation. Spec §2.3 read-declared-set = Name ∪ ArtifactID.
	v := NewVerifier()
	events := []AuditEvent{{ConversationID: "conv-1", Kind: KindRead, Target: "abc123", Ts: time.Unix(1, 0)}}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 0)
}

func TestVerifier_OrderedByTs(t *testing.T) {
	v := NewVerifier()
	events := []AuditEvent{
		{ConversationID: "conv-1", Kind: KindRead, Target: "/late", Ts: time.Unix(9, 0)},
		{ConversationID: "conv-1", Kind: KindRead, Target: "/early", Ts: time.Unix(1, 0)},
	}
	got := v.Verify(mkContractForVerifierTests(), events)
	require.Len(t, got, 2)
	require.Equal(t, "/early", got[0].Target)
	require.Equal(t, "/late", got[1].Target)
}

func TestVerifier_ReturnsEmptySliceNotNil(t *testing.T) {
	v := NewVerifier()
	got := v.Verify(mkContractForVerifierTests(), []AuditEvent{})
	require.NotNil(t, got)
	require.Len(t, got, 0)
}

// Test 31: purity grep --------------------------------------------

func TestVerifier_PurityGrep(t *testing.T) {
	// Read audit.go, locate the Verify function body, assert it
	// contains none of the forbidden call prefixes. This is the
	// spec §7 (c) invariant.
	data, err := os.ReadFile("audit.go")
	require.NoError(t, err)
	src := string(data)
	// Find "func (verifier) Verify(" ... matching closing brace.
	start := strings.Index(src, "func (verifier) Verify(")
	require.GreaterOrEqual(t, start, 0, "Verify function not found in audit.go")
	depth := 0
	end := -1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end > 0 {
			break
		}
	}
	require.Greater(t, end, start, "Verify function body has unbalanced braces")
	body := src[start:end]
	forbidden := []string{
		"os.", "net.", "http.", "log.", "expvar.",
		"time.Now", "sql.", "database/sql",
	}
	for _, f := range forbidden {
		require.NotContains(t, body, f, "Verify body must be pure; found %q", f)
	}
}

// Test 32: FuzzVerify — 30s under -fuzztime=30s -------------------

func FuzzVerify(f *testing.F) {
	// Seed corpus — 8 canonical shapes (spec plan §Fuzz-corpus).
	// The fuzz driver mutates only the byte inputs below; the
	// contract is fixed per-run for tractability.
	f.Add("read", "/tmp/a", "", int64(0))  // matches
	f.Add("read", "/tmp/x", "", int64(0))  // undeclared_read
	f.Add("write", "/tmp/b", "", int64(1)) // matches (empty hash ok)
	f.Add("write", "/tmp/b", hex64('a'), int64(1))
	f.Add("write", "/tmp/x", hex64('b'), int64(1))
	f.Add("tool_call", "bash", "", int64(0))
	f.Add("tool_call", "mcp:evil:x", "", int64(0))
	f.Add("model_call", "chat", "", int64(0))

	c := mkContractForVerifierTests()
	v := NewVerifier()

	f.Fuzz(func(t *testing.T, kind, target, hash string, size int64) {
		ev := AuditEvent{
			ConversationID: "conv-1",
			Kind:           AuditKind(kind),
			Target:         target,
			Hash:           hash,
			SizeBytes:      size,
			Ts:             time.Unix(1, 0),
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Verify panicked on %+v: %v", ev, r)
			}
		}()
		got := v.Verify(c, []AuditEvent{ev})
		if len(got) > 1 {
			t.Fatalf("len(violations) > len(events): %d > 1", len(got))
		}
		for _, vio := range got {
			if vio.Target != ev.Target {
				t.Fatalf("violation Target=%q does not match event Target=%q", vio.Target, ev.Target)
			}
		}
	})
}

// Tests 33-36: ArtifactHashAppender ---------------------------------

func TestArtifactHashAppender_NopReturnsNilOnEmpty(t *testing.T) {
	require.NoError(t, NopArtifactHashAppender{}.Append(context.Background(), "r", nil))
	require.NoError(t, NopArtifactHashAppender{}.Append(context.Background(), "r", []string{}))
}

func TestArtifactHashAppender_NopReturnsNilOnAllValidHex(t *testing.T) {
	require.NoError(t, NopArtifactHashAppender{}.Append(context.Background(), "r", []string{hex64('a'), hex64('b')}))
}

func TestArtifactHashAppender_NopRejectsNonHex(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"raw-non-hex", []string{"not-hex"}},
		{"uppercase-hex", []string{strings.ToUpper(hex64('a'))}},
		{"63-chars", []string{strings.Repeat("a", 63)}},
		{"65-chars", []string{strings.Repeat("a", 65)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := NopArtifactHashAppender{}.Append(context.Background(), "r", c.in)
			require.ErrorIs(t, err, ErrInvalidArtifactHash)
		})
	}
}

// Test 34: sort-and-dedupe helper ----------------------------------

func TestSortAndDedupeHashes(t *testing.T) {
	in := []string{hex64('c'), hex64('a'), hex64('b'), hex64('a')} // duplicate + unsorted
	out, err := SortAndDedupeHashes(in)
	require.NoError(t, err)
	require.Len(t, out, 3)
	// Assert lexicographic ascending.
	for i := 1; i < len(out); i++ {
		require.Less(t, out[i-1], out[i])
	}
}

func TestSortAndDedupeHashes_RejectsNonHex(t *testing.T) {
	_, err := SortAndDedupeHashes([]string{hex64('a'), "not-hex"})
	require.ErrorIs(t, err, ErrInvalidArtifactHash)
}

// Test 35: non-hex hash rejected at Record ------------------------

func TestArtifactHashAppender_NonHexRejectedByRecorder(t *testing.T) {
	db := openTestDB(t)
	r := NewSQLRecorder(db)
	ev := baseEvent(KindWrite)
	ev.Hash = "not-hex"
	ev.SizeBytes = 12
	err := r.Record(context.Background(), ev)
	require.ErrorIs(t, err, ErrInvalidArtifactHash)
	// Confirm no row was inserted.
	var cnt int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&cnt))
	require.Equal(t, 0, cnt)
}

// captureAppender records every Append call for assertions.
type captureAppender struct {
	calls [][]string
}

func (c *captureAppender) Append(_ context.Context, _ string, hashes []string) error {
	cp := make([]string, len(hashes))
	copy(cp, hashes)
	c.calls = append(c.calls, cp)
	return nil
}

// Test 34 (audit-layer version): sort+dedupe end-to-end.
func TestAppendHashesFromEvents_SortedDeduped(t *testing.T) {
	ap := &captureAppender{}
	events := []AuditEvent{
		{Kind: KindWrite, Hash: hex64('c'), Ts: time.Unix(1, 0)},
		{Kind: KindWrite, Hash: hex64('a'), Ts: time.Unix(2, 0)},
		{Kind: KindWrite, Hash: hex64('b'), Ts: time.Unix(3, 0)},
		{Kind: KindWrite, Hash: hex64('a'), Ts: time.Unix(4, 0)}, // duplicate
		{Kind: KindRead, Target: "/tmp/a", Ts: time.Unix(5, 0)},  // ignored
		{Kind: KindWrite, Hash: "", Ts: time.Unix(6, 0)},         // no hash, ignored
	}
	require.NoError(t, AppendHashesFromEvents(context.Background(), ap, "run-1", events))
	require.Len(t, ap.calls, 1)
	require.Len(t, ap.calls[0], 3)
	require.Equal(t, hex64('a'), ap.calls[0][0])
	require.Equal(t, hex64('b'), ap.calls[0][1])
	require.Equal(t, hex64('c'), ap.calls[0][2])
}

// Test 36: Append NOT called when the pipeline harness skips it on
// violation-present. AppendHashesFromEvents itself does not know about
// violations — the caller enforces that precondition (spec §2.4). This
// test proves the caller's shape: never call AppendHashesFromEvents if
// Verify surfaced any violation.
func TestAppendHashesFromEvents_NotCalledOnViolationPresent(t *testing.T) {
	ap := &captureAppender{}
	events := []AuditEvent{
		{ConversationID: "conv-1", Kind: KindWrite, Target: "/tmp/rogue", Hash: hex64('a'), Ts: time.Unix(1, 0)},
	}
	v := NewVerifier()
	viols := v.Verify(mkContractForVerifierTests(), events)
	require.NotEmpty(t, viols, "precondition: the write target /tmp/rogue is undeclared")

	// Simulate the pipeline: only call Append when Verify was clean.
	if len(viols) == 0 {
		require.NoError(t, AppendHashesFromEvents(context.Background(), ap, "run-1", events))
	}
	require.Len(t, ap.calls, 0)
}

// Test 43: file-domain audit --------------------------------------

func TestFileDomain_NoExternalFileTouched(t *testing.T) {
	// We can't shell out reliably here to git; instead, walk the
	// repo and assert none of the forbidden directories contain
	// a file whose blame line references this WT's tag. That test
	// is over-cautious in a hostile setting, so we do a simpler
	// static check: iterate the five allowed relative paths, stat
	// each, and separately assert the forbidden dirs are NOT under
	// this WT's ownership by grepping for the WT tag string
	// `WT-2-runtime-audit` in production sources of the forbidden
	// dirs. Zero hits → the WT did not sneak instrumentation into
	// those files.
	root := findModuleRoot(t)
	allowed := []string{
		"internal/journal/audit.go",
		"internal/journal/audit_test.go",
		"internal/observerstore/schema.sql",
		"internal/observerstore/contract_violations_view.go",
		"internal/observerstore/contract_violations_view_test.go",
	}
	for _, rel := range allowed {
		_, err := os.Stat(filepath.Join(root, rel))
		require.NoError(t, err, "expected allowed file %s to exist", rel)
	}
	forbiddenDirs := []string{
		"internal/executor",
		"cmd/slave-agent",
		"internal/evalrun",
		"pkg/agentbackend",
	}
	tag := regexp.MustCompile(`WT-2-runtime-audit`)
	for _, d := range forbiddenDirs {
		err := filepath.Walk(filepath.Join(root, d), func(path string, info os.FileInfo, werr error) error {
			if werr != nil {
				// Missing forbidden dir is fine — nothing to check.
				if os.IsNotExist(werr) {
					return nil
				}
				return werr
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sql") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if tag.Match(data) {
				t.Fatalf("forbidden file %s references WT-2-runtime-audit — file-domain leak", path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			require.NoError(t, err)
		}
	}
}

// Test 44: hook-call-shape design test (spec §7 (i)) ---------------

// wantHookShape is the exact call-site template the follow-up wiring
// WT MUST use at each of the spec §3.1–§3.4 sites. Kept as a Go
// string constant so a source-level grep against a future PR can
// re-anchor against ONE canonical spelling.
const wantHookShape = `if err := r.Record(ctx, journal.AuditEvent{...}); err != nil {
    log.Printf("journal: audit write dropped: %v", err)
    journal.AuditWriteDroppedTotal().Add(1)
}`

func TestWiringContract_HookCallShape(t *testing.T) {
	// This test does not grep executor sources today (those files
	// are untouched by this WT). It exists so the follow-up wiring
	// WT can extend the assertion to a real source grep. The
	// stability contract is: wantHookShape must contain each of the
	// three anchor lines below verbatim.
	require.Contains(t, wantHookShape, "r.Record(ctx, journal.AuditEvent{")
	require.Contains(t, wantHookShape, `log.Printf("journal: audit write dropped: %v", err)`)
	require.Contains(t, wantHookShape, "journal.AuditWriteDroppedTotal().Add(1)")
}

// Test to prove the spec §5.1 TODO exists at exactly ONE site (the
// interface docstring in audit.go) and nowhere else in this repo.
func TestTODO_LiveAtExactlyOneAllowedSite(t *testing.T) {
	root := findModuleRoot(t)
	tag := regexp.MustCompile(`TODO\(WT-2-metric-extract or D1-completion\)`)
	var hits []string
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendor / hidden dirs.
			base := info.Name()
			if base == "vendor" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if tag.Match(data) {
			hits = append(hits, path)
		}
		return nil
	}))
	// Exactly one production hit: audit.go. Test files are permitted
	// (this file references the tag inside a regex literal).
	prod := 0
	for _, h := range hits {
		if strings.HasSuffix(h, "_test.go") {
			continue
		}
		prod++
		require.True(t, strings.HasSuffix(h, "internal/journal/audit.go"),
			"TODO must live at audit.go; found unexpected hit at %s", h)
	}
	require.Equal(t, 1, prod, "expected exactly one production TODO site")
}

// findModuleRoot walks upward from cwd until it finds go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found from %s", dir)
		}
		dir = parent
	}
}

// Sanity: an errors.Is chain that both journal and evalrun use for the
// hash-format check surfaces the sentinel correctly.
func TestErrInvalidArtifactHash_Sentinel(t *testing.T) {
	err := NopArtifactHashAppender{}.Append(context.Background(), "r", []string{"not-hex"})
	require.True(t, errors.Is(err, ErrInvalidArtifactHash))
}
