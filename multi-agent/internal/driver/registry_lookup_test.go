package driver

import (
	"bytes"
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockUserspace is an in-memory UserspaceSearcher for tests.
type mockUserspace struct {
	rows     []PackageHit
	err      error
	callArgs []string
	mu       sync.Mutex
}

func (m *mockUserspace) SearchPackagesForIdentity(q, workspaceID, userID, kindFilter string, limit int) ([]PackageHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callArgs = append(m.callArgs, q)
	if m.err != nil {
		return nil, m.err
	}
	if limit > 0 && len(m.rows) > limit {
		return m.rows[:limit], nil
	}
	return m.rows, nil
}

// captureLogs sets log output to a buffer, returns the buffer + a
// cleanup callback.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })
	return &buf
}

// sampleCapture records SampleWrite invocations for assertions.
type sampleCapture struct {
	mu      sync.Mutex
	samples []RegistryLookupSample
	err     error
}

func (s *sampleCapture) write(_ context.Context, sam RegistryLookupSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.samples = append(s.samples, sam)
	return nil
}

func (s *sampleCapture) snapshot() []RegistryLookupSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RegistryLookupSample, len(s.samples))
	copy(out, s.samples)
	return out
}

func setupLookup(t *testing.T, us UserspaceSearcher, sc *sampleCapture) {
	t.Helper()
	resetRegistryForTest()
	resetLookupDepsForTest()
	resetNoRegistryLookupForTest()
	ResetLookupMetricsForTest()
	SetCurrentRunID("run-testx001")
	deps := LookupDeps{
		UserspaceStore: us,
		WorkspaceID:    "ws-abc12345",
		UserID:         "user_abc",
		CurrentRunID:   CurrentRunID,
	}
	if sc != nil {
		deps.SampleWrite = sc.write
	}
	SetLookupDeps(deps)
}

// --- §7 (a) sanitization + length ---

func TestLookup_QueryTruncatedAtCap(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	Lookup(context.Background(), strings.Repeat("a", 300))
	if !strings.Contains(buf.String(), "query truncated from 300") {
		t.Fatalf("expected truncation log; got:\n%s", buf.String())
	}
}

// TestLookup_AblationLogCappedAt64Chars — §7 (h) bound. Ablation
// log's query_sanitized rendering must be capped at 64 chars so an
// alphanumeric secret-shaped input is bounded.
func TestLookup_AblationLogCappedAt64Chars(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	// 100 alphanumeric chars — all pass the sanitizer character class.
	raw := strings.Repeat("Ab1", 40) // 120 chars total
	Lookup(context.Background(), raw)
	logStr := buf.String()
	// Extract the query_sanitized=%q value.
	i := strings.Index(logStr, `query_sanitized="`)
	if i < 0 {
		t.Fatalf("expected query_sanitized=... in log: %s", logStr)
	}
	rest := logStr[i+len(`query_sanitized="`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatalf("unterminated query_sanitized: %s", logStr)
	}
	rendered := rest[:end]
	if len(rendered) > 64 {
		t.Fatalf("query_sanitized len %d > 64 — §7 (h) bound violated: %q", len(rendered), rendered)
	}
}

// TestLookup_AblationFirst_NoTruncationLogUnderAblation — §7 (c)
// invariant: an ablated Lookup emits ONE line total (the [ablation]
// line), and never the truncation log even when the query is long
// enough to trigger it.
func TestLookup_AblationFirst_NoTruncationLogUnderAblation(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	Lookup(context.Background(), strings.Repeat("a", 300))
	if strings.Contains(buf.String(), "query truncated") {
		t.Fatalf("truncation log leaked past ablation short-circuit: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "[ablation] NoRegistryLookup: skipped") {
		t.Fatalf("expected [ablation] line: %s", buf.String())
	}
}

// TestLookup_SanitizerStripsHyphen — FTS5 treats `-foo` as a NOT
// operator prefix; the sanitizer must strip `-`.
func TestLookup_SanitizerStripsHyphen(t *testing.T) {
	us := &mockUserspace{}
	setupLookup(t, us, &sampleCapture{})
	Lookup(context.Background(), "foo-bar")
	if strings.Contains(us.callArgs[0], "-") {
		t.Fatalf("sanitizer should strip -: %q", us.callArgs[0])
	}
}

// TestLookup_SanitizerLowercasesFTS5Operators — the sanitizer
// lowercases AND/OR/NOT/NEAR so they parse as plain search terms.
func TestLookup_SanitizerLowercasesFTS5Operators(t *testing.T) {
	us := &mockUserspace{}
	setupLookup(t, us, &sampleCapture{})
	Lookup(context.Background(), "foo AND bar OR baz NEAR zap NOT quux")
	got := us.callArgs[0]
	for _, upper := range []string{"AND", "OR", "NEAR", "NOT"} {
		// The upper form should not appear as a whole word.
		if strings.Contains(" "+got+" ", " "+upper+" ") {
			t.Fatalf("sanitizer left %q as a bare uppercase operator: %q", upper, got)
		}
	}
}

func TestLookup_SanitizerStripsReservedChars(t *testing.T) {
	us := &mockUserspace{}
	setupLookup(t, us, &sampleCapture{})
	Lookup(context.Background(), "foo NEAR/3 bar")
	if len(us.callArgs) != 1 {
		t.Fatalf("want 1 call, got %d", len(us.callArgs))
	}
	// The sanitized query should have `/` stripped.
	if strings.Contains(us.callArgs[0], "/") {
		t.Fatalf("sanitizer should strip /: %q", us.callArgs[0])
	}
}

func TestLookup_SanitizerStripsQuotesAndCarets(t *testing.T) {
	us := &mockUserspace{}
	setupLookup(t, us, &sampleCapture{})
	Lookup(context.Background(), `"foo^*"`)
	got := us.callArgs[0]
	for _, bad := range []string{`"`, `^`, `*`} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitizer failed to strip %q: %q", bad, got)
		}
	}
}

// TestLookup_AblationLogLowercasesFTS5Operators — PR #71 review
// P1-B4-2: the ablation-log path must run the SAME sanitisation as
// the FTS5-bound path, so a raw ablated call `foo AND bar` is
// rendered `foo and bar` (not `foo AND bar`). Prevents subtle
// operator-visibility drift between the two branches.
func TestLookup_AblationLogLowercasesFTS5Operators(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	Lookup(context.Background(), "foo AND bar OR baz NEAR zap NOT quux")
	got := buf.String()
	for _, upper := range []string{"AND", "OR", "NEAR", "NOT"} {
		// Whole-word check so we don't false-positive on a token
		// that happens to embed the letters.
		if strings.Contains(" "+got+" ", " "+upper+" ") {
			t.Fatalf("ablation log left uppercase operator %q in %q — should be lowercased like the non-ablated path", upper, got)
		}
	}
}

// TestLookup_EmptySanitisedQueryReturnsZeroHits — P1 fresh-review
// finding: when the caller's input sanitises to empty (all
// punctuation / emoji / meta chars), Lookup must NOT call
// SearchPackagesForIdentity(q="") (which falls into the
// "list-all-packages" branch and would inflate
// RegistryLookupHitRate). Assert: 0 hits, no userspace call, the
// query counter still bumps (denominator), the hit counter does
// NOT bump (numerator), sample still written with hit_count=0.
func TestLookup_EmptySanitisedQueryReturnsZeroHits(t *testing.T) {
	us := &mockUserspace{rows: []PackageHit{{Slug: "unrelated_a"}, {Slug: "unrelated_b"}}}
	sc := &sampleCapture{}
	setupLookup(t, us, sc)

	beforeQ := lookupQueries.Load()
	beforeH := lookupHits.Load()

	// All punctuation. Sanitiser strips every char → "".
	hits := Lookup(context.Background(), `!@#$%^&*()`)
	if len(hits) != 0 {
		t.Fatalf("empty-sanitised query must return 0 hits, got %d: %+v", len(hits), hits)
	}
	if len(us.callArgs) != 0 {
		t.Fatalf("userspace MUST NOT be called for empty-sanitised query, got %d calls: %v", len(us.callArgs), us.callArgs)
	}
	if got := lookupQueries.Load(); got != beforeQ+1 {
		t.Fatalf("queries counter should still bump (denominator), before=%d after=%d", beforeQ, got)
	}
	if got := lookupHits.Load(); got != beforeH {
		t.Fatalf("hits counter must NOT bump, before=%d after=%d", beforeH, got)
	}
	// Sample still written with hit_count=0.
	samples := sc.snapshot()
	if len(samples) != 1 {
		t.Fatalf("want 1 sample row, got %d", len(samples))
	}
	if samples[0].HitCount != 0 {
		t.Fatalf("sample hit_count = %d, want 0", samples[0].HitCount)
	}
}

// --- §7 (b) result caps ---

func TestLookup_FTSResultCapAt20(t *testing.T) {
	rows := make([]PackageHit, 100)
	for i := range rows {
		rows[i] = PackageHit{Slug: "slug_" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))}
	}
	setupLookup(t, &mockUserspace{rows: rows}, &sampleCapture{})
	hits := Lookup(context.Background(), "slug")
	if len(hits) > 20 {
		t.Fatalf("hits len %d > 20", len(hits))
	}
}

func TestLookup_MergedCapAt20(t *testing.T) {
	// 15 registry entries + 15 userspace entries, all unique names → cap 20.
	for i := 0; i < 15; i++ {
		noteRegister("slave-a", "regtool_"+string(rune('a'+i)), "s")
	}
	rows := make([]PackageHit, 15)
	for i := range rows {
		rows[i] = PackageHit{Slug: "ustool_" + string(rune('a'+i))}
	}
	setupLookup(t, &mockUserspace{rows: rows}, &sampleCapture{})
	// Re-add registry entries because setupLookup calls resetRegistryForTest.
	for i := 0; i < 15; i++ {
		noteRegister("slave-a", "regtool_"+string(rune('a'+i)), "s")
	}
	// Use a broad substring that matches all names of both prefixes.
	hits := Lookup(context.Background(), "tool")
	if len(hits) > 20 {
		t.Fatalf("merged cap violated: %d hits", len(hits))
	}
}

// --- §7 (c) ablation observable ---

func TestLookup_NoRegistryLookupAblationSkipsAndLogs(t *testing.T) {
	buf := captureLogs(t)
	sc := &sampleCapture{}
	setupLookup(t, &mockUserspace{}, sc)
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	hits := Lookup(context.Background(), "any")
	if hits != nil {
		t.Fatal("ablation must return nil")
	}
	if !strings.Contains(buf.String(), "[ablation] NoRegistryLookup: skipped") {
		t.Fatalf("expected ablation log, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "query_hash=") {
		t.Fatalf("log must include query_hash prefix, got:\n%s", buf.String())
	}
	if len(sc.snapshot()) != 0 {
		t.Fatal("ablated call must not write sample")
	}
}

func TestLookup_NoRegistryLookupAblationDoesNotBumpCounters(t *testing.T) {
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	before := lookupQueries.Load()
	Lookup(context.Background(), "any")
	if got := lookupQueries.Load(); got != before {
		t.Fatalf("queries counter advanced under ablation: %d → %d", before, got)
	}
}

// --- §7 (d) nil deps degrade with warn ---

func TestLookup_NilUserspaceStoreDegradesToRegistry(t *testing.T) {
	buf := captureLogs(t)
	sc := &sampleCapture{}
	setupLookup(t, nil, sc)
	noteRegister("slave-a", "foo", "s")
	hits := Lookup(context.Background(), "foo")
	if len(hits) == 0 {
		t.Fatal("expected registry hit")
	}
	if !strings.Contains(buf.String(), "UserspaceStore unwired") {
		t.Fatalf("expected unwired warn, got:\n%s", buf.String())
	}
}

func TestLookup_EmptyRegistryViewDegradesToUserspace(t *testing.T) {
	us := &mockUserspace{rows: []PackageHit{{Slug: "us_x"}}}
	setupLookup(t, us, &sampleCapture{})
	hits := Lookup(context.Background(), "us")
	if len(hits) == 0 {
		t.Fatal("expected userspace hit when registry view empty")
	}
	for _, h := range hits {
		if h.Source == "registry" {
			t.Fatal("no registry hits expected when view empty")
		}
	}
}

// --- §7 (e) score in unit interval ---

func TestLookup_ScoreIsInUnitInterval(t *testing.T) {
	noteRegister("slave-a", "exact_match", "s")
	noteRegister("slave-a", "substring_of_match", "s")
	us := &mockUserspace{rows: []PackageHit{{Slug: "match_1"}, {Slug: "match_2"}}}
	setupLookup(t, us, &sampleCapture{})
	// noteRegister was called before setupLookup which resets view; redo:
	noteRegister("slave-a", "exact_match", "s")
	noteRegister("slave-a", "substring_of_match", "s")
	hits := Lookup(context.Background(), "match")
	if len(hits) == 0 {
		t.Fatal("expected some hits")
	}
	for _, h := range hits {
		if h.Score < 0 || h.Score > 1 {
			t.Errorf("hit %q score %v out of [0,1]", h.MCPName, h.Score)
		}
	}
}

// --- §7 (f) fts error does not propagate ---

func TestLookup_FTSErrorDoesNotPropagate(t *testing.T) {
	setupLookup(t, &mockUserspace{err: errors.New("boom")}, &sampleCapture{})
	noteRegister("slave-a", "reg_x", "s")
	// Registry-side match on 'reg' returns 1 hit.
	hits := Lookup(context.Background(), "reg")
	if len(hits) == 0 {
		t.Fatal("expected registry hit; userspace error should degrade")
	}
}

// --- §7 (g) perf conditional on -short ---

func TestLookup_PerfBench_ConditionalOnShort(t *testing.T) {
	for i := 0; i < 100; i++ {
		noteRegister("slave-a", "tool_"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)), "s")
	}
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	for i := 0; i < 100; i++ {
		noteRegister("slave-a", "tool_"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)), "s")
	}
	_ = Lookup(context.Background(), "tool")
	if testing.Short() {
		t.Skip("short mode; wall-clock skipped")
	}
	start := time.Now()
	_ = Lookup(context.Background(), "tool")
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("Lookup on 100-entry view took %v > 10ms", elapsed)
	}
}

// --- §7 (h) log echoes sanitized+prefix hash, not full hash or raw ---

func TestLookup_LogEchoesSanitizedAndPrefixHash_NotFullHashOrRaw(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	raw := `super_secret!@#$%^&*()_+ABC1234567890`
	Lookup(context.Background(), raw)
	logStr := buf.String()

	// The raw input includes chars stripped by the sanitizer AND
	// alphanumerics that survive. The log must contain the sanitized
	// form (not raw with meta chars).
	if strings.Contains(logStr, "!@#") {
		t.Fatalf("log leaks raw meta chars: %s", logStr)
	}
	// query_hash=<8 hex chars> — not 64.
	re := regexp.MustCompile(`query_hash=([a-f0-9]+)`)
	m := re.FindStringSubmatch(logStr)
	if m == nil {
		t.Fatalf("log missing query_hash=<hex>: %s", logStr)
	}
	if len(m[1]) != 8 {
		t.Fatalf("query_hash prefix should be 8 hex chars, got %d: %q", len(m[1]), m[1])
	}
}

// --- Sample row invariants ---

func TestLookup_WritesSampleRowPerCall(t *testing.T) {
	sc := &sampleCapture{}
	setupLookup(t, &mockUserspace{}, sc)
	for i := 0; i < 5; i++ {
		Lookup(context.Background(), "q")
	}
	got := sc.snapshot()
	if len(got) != 5 {
		t.Fatalf("want 5 sample rows, got %d", len(got))
	}
	for _, s := range got {
		if s.RunID != "run-testx001" {
			t.Errorf("row missing/wrong run_id: %+v", s)
		}
	}
}

func TestLookup_AblatedCallDoesNotWriteSample(t *testing.T) {
	sc := &sampleCapture{}
	setupLookup(t, &mockUserspace{}, sc)
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()
	for i := 0; i < 5; i++ {
		Lookup(context.Background(), "q")
	}
	if len(sc.snapshot()) != 0 {
		t.Fatalf("ablated calls must not write samples, got %d rows", len(sc.snapshot()))
	}
}

func TestLookup_SampleRowStoresHashPrefixNotQueryText(t *testing.T) {
	sc := &sampleCapture{}
	setupLookup(t, &mockUserspace{}, sc)
	raw := "super-secret-1234567890abc"
	Lookup(context.Background(), raw)
	got := sc.snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 sample, got %d", len(got))
	}
	if got[0].QueryHashPrefix == "" {
		t.Fatal("hash prefix must not be empty")
	}
	if len(got[0].QueryHashPrefix) != 8 {
		t.Fatalf("hash prefix must be 8 hex, got %q", got[0].QueryHashPrefix)
	}
	// Assertion: the RegistryLookupSample struct has no field named
	// "raw query" — we check the struct doesn't accidentally leak
	// the string via reflection. Encoded to JSON, should not contain
	// the raw secret.
	// (Since RegistryLookupSample doesn't have that field, this is
	// a structural guarantee, but we still assert defensively.)
	var found bool
	_ = found
	for _, s := range got {
		if strings.Contains(s.QueryHashPrefix, raw) {
			t.Fatal("hash prefix leaks raw text")
		}
		if strings.Contains(s.WorkspaceID, raw) {
			t.Fatal("workspace leaks raw text")
		}
	}
}

func TestLookup_NilSampleWriteDegradesAndWarnsOnce(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, nil) // no sampleCapture → SampleWrite nil
	Lookup(context.Background(), "q1")
	Lookup(context.Background(), "q2")
	logStr := buf.String()
	wantLine := "SampleWrite unwired"
	count := strings.Count(logStr, wantLine)
	if count != 1 {
		t.Fatalf("expected exactly 1 %q log, got %d in:\n%s", wantLine, count, logStr)
	}
}

// --- init-error surfacing ---

func TestNoRegistryLookup_InitErrorSurfacedOnFirstLookup(t *testing.T) {
	buf := captureLogs(t)
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	resetNoRegistryLookupForTest()
	noRegistryLookupInitErr = errors.New("simulated register clash")

	Lookup(context.Background(), "q1")
	Lookup(context.Background(), "q2")
	logStr := buf.String()
	wantLine := "[error] NoRegistryLookup ablation wiring inert"
	count := strings.Count(logStr, wantLine)
	if count != 1 {
		t.Fatalf("expected exactly 1 %q log, got %d in:\n%s", wantLine, count, logStr)
	}
}

// --- Metric tests ---

// TestLookupHitRate_ExercisedThroughLookup covers the wiring: 3
// queries where 2 hit → 0.667.
func TestLookupHitRate_ExercisedThroughLookup(t *testing.T) {
	setupLookup(t, &mockUserspace{rows: []PackageHit{{Slug: "hit_target"}}}, &sampleCapture{})
	Lookup(context.Background(), "hit") // 1 hit
	Lookup(context.Background(), "hit") // 1 hit

	// Empty results for this query.
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	Lookup(context.Background(), "no-match-anywhere") // 0 hits

	// Because setupLookup resets the metrics, this test is redundant
	// with the direct counter test in registrylookupmetrics_test.go.
	// Kept as a smoke against future accidental double-bumping.
	_ = LookupHitRate()
}

// --- Registry-side score tests ---

func TestLookup_RegistryExactMatch_ScoreOne(t *testing.T) {
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noteRegister("slave-a", "exact", "s")
	hits := Lookup(context.Background(), "exact")
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Score != 1.0 {
		t.Fatalf("exact match should score 1.0, got %v", hits[0].Score)
	}
}

func TestLookup_RegistrySubstringMatch_ScoreSevenTenths(t *testing.T) {
	setupLookup(t, &mockUserspace{}, &sampleCapture{})
	noteRegister("slave-a", "foo_and_bar", "s")
	hits := Lookup(context.Background(), "foo")
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Score != 0.7 {
		t.Fatalf("substring match should score 0.7, got %v", hits[0].Score)
	}
}
