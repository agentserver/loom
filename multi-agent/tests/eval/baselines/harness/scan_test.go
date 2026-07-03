package harness

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestScanTreeForSecrets_DetectsSK — plan #14. Spec §7(b) core contract:
// a text file with an sk-... token is Flagged (and NOT in SafeToUpload).
func TestScanTreeForSecrets_DetectsSK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notebook.py"), []byte("api_key = 'sk-testonly-secret-1234567890AB'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanTreeForSecrets: %v", err)
	}
	if !slices.Contains(report.Flagged, "notebook.py") {
		t.Errorf("expected sk-... to be Flagged; got %+v", report)
	}
	if slices.Contains(report.SafeToUpload, "notebook.py") {
		t.Errorf("flagged file must NOT be in SafeToUpload; got %+v", report)
	}
}

// TestScanTreeForSecrets_DetectsJWTAndAWS — plan #15.
func TestScanTreeForSecrets_DetectsJWTAndAWS(t *testing.T) {
	dir := t.TempDir()
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.foo.bar"
	aws := "AKIA1234567890EXAMPLE"
	if err := os.WriteFile(filepath.Join(dir, "token.txt"), []byte(jwt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creds.txt"), []byte(aws), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanTreeForSecrets: %v", err)
	}
	if len(report.Flagged) != 2 {
		t.Fatalf("expected 2 Flagged, got %d: %+v", len(report.Flagged), report)
	}
}

// TestScanTreeForSecrets_SkipsBinary — plan #16. Binary files land in
// SkippedBinary (NOT in SafeToUpload — see spec §7(b) "skipped ≠ safe").
// A follow-up would need an --allow-binary-upload escape hatch to ship
// them; this worktree refuses.
func TestScanTreeForSecrets_SkipsBinary(t *testing.T) {
	dir := t.TempDir()
	// NUL in first 512 bytes → treated as binary.
	content := append([]byte{0, 0, 0, 0}, []byte("sk-testonly-secret-1234567890AB")...)
	if err := os.WriteFile(filepath.Join(dir, "artifact.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanTreeForSecrets: %v", err)
	}
	if !slices.Contains(report.SkippedBinary, "artifact.bin") {
		t.Errorf("binary file should be in SkippedBinary; got %+v", report)
	}
	if slices.Contains(report.SafeToUpload, "artifact.bin") {
		t.Errorf("binary file MUST NOT be in SafeToUpload (would be uploaded unscanned!); got %+v", report)
	}
	if slices.Contains(report.Flagged, "artifact.bin") {
		t.Errorf("binary file should not be Flagged either; got %+v", report)
	}
}

// TestScanTreeForSecrets_SkipsLargeFile — plan #17. Same "skipped ≠ safe"
// rule applies: a 9 MiB file lands in SkippedLarge, never SafeToUpload.
func TestScanTreeForSecrets_SkipsLargeFile(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, 9*1024*1024)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, "big.log"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanTreeForSecrets: %v", err)
	}
	if !slices.Contains(report.SkippedLarge, "big.log") {
		t.Errorf("large file should be in SkippedLarge; got %+v", report)
	}
	if slices.Contains(report.SafeToUpload, "big.log") {
		t.Errorf("large file MUST NOT be in SafeToUpload; got %+v", report)
	}
}

// TestScanTreeForSecrets_DetectsSecretPastRune256 — regression for the
// P0 fresh-review finding: secretscrub.Sanitize truncates its output
// at 256 runes, so a naive `strings.Count(out, "[REDACTED]") -
// strings.Count(src, "[REDACTED]")` misses any secret located past
// rune 256 (the marker gets lopped off, the count differential is 0,
// and the file is classified SafeToUpload — uploaded to E2B with the
// leak intact). The chunked scan fixes this by running Sanitize on
// overlapping windows small enough that truncation never fires.
func TestScanTreeForSecrets_DetectsSecretPastRune256(t *testing.T) {
	dir := t.TempDir()
	// 300 runes of filler + a real sk-shaped token past rune 256.
	body := make([]byte, 300)
	for i := range body {
		body[i] = 'A'
	}
	body = append(body, []byte(" sk-testonly-secret-1234567890AB")...)
	if err := os.WriteFile(filepath.Join(dir, "leaky.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatalf("ScanTreeForSecrets: %v", err)
	}
	if !slices.Contains(rep.Flagged, "leaky.txt") {
		t.Fatalf("secret past rune 256 not flagged (P0 leak); report=%+v", rep)
	}
	if slices.Contains(rep.SafeToUpload, "leaky.txt") {
		t.Fatalf("secret past rune 256 marked SafeToUpload (P0 leak); report=%+v", rep)
	}
}

// TestScanTreeForSecrets_LiteralRedactedInFixture_NoFalsePositive —
// a workload fixture (docs, README) that legitimately contains the
// literal `[REDACTED]` string must not be false-flagged.
func TestScanTreeForSecrets_LiteralRedactedInFixture_NoFalsePositive(t *testing.T) {
	dir := t.TempDir()
	body := "This doc explains redaction. Values like [REDACTED] appear in outputs."
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rep.Flagged, "doc.md") {
		t.Errorf("literal [REDACTED] in fixture should not be Flagged; got %+v", rep)
	}
	if !slices.Contains(rep.SafeToUpload, "doc.md") {
		t.Errorf("clean doc should be SafeToUpload; got %+v", rep)
	}
}

// TestScanTreeForSecrets_SecretAtChunkBoundary — a secret that
// straddles a window boundary must still be caught thanks to overlap.
func TestScanTreeForSecrets_SecretAtChunkBoundary(t *testing.T) {
	dir := t.TempDir()
	// Position the secret so it straddles the first window boundary.
	// scanChunkRunes=200; center a 32-byte token at rune ~180.
	body := make([]byte, 180)
	for i := range body {
		body[i] = 'a'
	}
	body = append(body, []byte("sk-testonly-secret-1234567890AB")...)
	// Padding after so the total is > 200 (ensures chunking triggers).
	for i := 0; i < 100; i++ {
		body = append(body, 'a')
	}
	if err := os.WriteFile(filepath.Join(dir, "boundary.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Flagged, "boundary.txt") {
		t.Errorf("secret at chunk boundary must be flagged (overlap contract); got %+v", rep)
	}
}

// TestScanTreeForSecrets_CleanFixtureIsSafeToUpload — sanity: clean
// text files land in SafeToUpload, empty Flagged / Skipped.
func TestScanTreeForSecrets_CleanFixtureIsSafeToUpload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ScanTreeForSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Flagged) != 0 {
		t.Errorf("clean fixture Flagged: %+v", report)
	}
	if !slices.Contains(report.SafeToUpload, "readme.md") {
		t.Errorf("clean file should be SafeToUpload; got %+v", report)
	}
}
