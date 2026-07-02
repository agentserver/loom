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
