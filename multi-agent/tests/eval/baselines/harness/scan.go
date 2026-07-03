package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// ErrPreUploadSecretDetected is returned by ScanTreeForSecrets (surfaced
// by the cloud baseline's Prepare) when any file in the workspace holds
// bytes that match a secretscrub pattern. Spec §7(b) — this is the
// worktree's single most dangerous failure mode; a match halts the run
// with exit 2 before any upload.
var ErrPreUploadSecretDetected = errors.New("harness: pre-upload secret scan detected potential secret")

// Byte thresholds for scan admission — spec §7(b) invariants. Files
// outside these bounds are skipped rather than flagged; a workload that
// wants to ship larger or binary fixtures must add an explicit escape
// hatch (not this worktree).
const (
	scanMaxFileSize          = 8 << 20 // 8 MiB
	scanBinarySniffBytes     = 512
	scanBinaryProbeAdmission = scanBinarySniffBytes
)

// Chunking parameters for scanChunked. Sanitize truncates output to 256
// runes (see internal/secretscrub/scrub.go maxRunes); running Sanitize
// on windows of scanChunkRunes < 256 guarantees the `[REDACTED]` marker
// survives, so a count-differential detects redactions reliably.
// scanChunkOverlap is the rune-length of the largest plausible secret
// (`-----BEGIN … PRIVATE KEY-----` is ~40 runes; other patterns bounded
// by their `{n,}` tails). Chosen well above every pattern's max to
// guarantee a straddling secret lands entirely inside at least one
// non-truncated window.
const (
	scanChunkRunes   = 200
	scanChunkOverlap = 128
)

// redactedMarker is what secretscrub.Sanitize substitutes for a matched
// secret. Counting occurrences of this literal in a Sanitize output vs.
// its input tells us whether the regex fired.
const redactedMarker = "[REDACTED]"

// scanChunked returns true iff any window of scanChunkRunes runes in
// `src`, run through secretscrub.Sanitize, gained at least one
// `[REDACTED]` marker over what the same window's input already
// contained. Because every window is ≤ scanChunkRunes < the Sanitize
// truncation cap, the marker is never truncated away — the P0 defect
// where a secret past rune 256 slipped through as SafeToUpload is
// closed. Windows overlap by scanChunkOverlap runes so a secret that
// straddles a boundary still lands entirely inside at least one window.
func scanChunked(src string) bool {
	runes := []rune(src)
	if len(runes) == 0 {
		return false
	}
	if len(runes) <= scanChunkRunes {
		return sanitizeAddedMarker(string(runes))
	}
	stride := scanChunkRunes - scanChunkOverlap // must be > 0; enforced by consts
	for start := 0; start < len(runes); start += stride {
		end := start + scanChunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		if sanitizeAddedMarker(string(runes[start:end])) {
			return true
		}
		if end == len(runes) {
			break
		}
	}
	return false
}

// sanitizeAddedMarker returns true iff Sanitize(window) contains more
// `[REDACTED]` markers than `window` itself did — i.e. Sanitize added
// at least one. Handles the false-positive from a fixture that
// legitimately contains the literal `[REDACTED]`.
func sanitizeAddedMarker(window string) bool {
	return strings.Count(secretscrub.Sanitize(window), redactedMarker) >
		strings.Count(window, redactedMarker)
}

// ScanReport is the full outcome of a workspace scan: files that
// matched a secretscrub pattern (Flagged), files that were admitted as
// safe to upload (SafeToUpload), and files the scanner deliberately did
// NOT scan (SkippedBinary + SkippedLarge). Cloud baselines MUST upload
// only files listed in SafeToUpload — treating skipped files as safe
// would let a binary blob past the scan into the cloud sandbox, which
// is a P0 leak path (spec §7(b)): a NUL-containing file could still
// hold recognisable API keys further in.
type ScanReport struct {
	// Flagged: files whose bytes matched the secretscrub regex family.
	// Non-empty ⇒ ErrPreUploadSecretDetected on the first entry.
	Flagged []string
	// SafeToUpload: files that were fully scanned AND passed. The cloud
	// baseline iterates this set (only) when POSTing to the sandbox.
	SafeToUpload []string
	// SkippedBinary / SkippedLarge: files not scanned because they hit
	// an admission gate. They are NOT uploaded either — a workload that
	// needs to ship binary/large fixtures must land an explicit escape
	// hatch (not this worktree).
	SkippedBinary []string
	SkippedLarge  []string
}

// ScanTreeForSecrets walks `root` and returns a ScanReport. Any file
// whose bytes trip a secretscrub pattern lands in Flagged; any file
// that fully passed lands in SafeToUpload; binary / oversized files
// land in the corresponding Skipped slice.
//
// Admission gates (§7(b) invariants):
//   - files > 8 MiB → SkippedLarge (not scanned, not uploadable)
//   - first 512 bytes contain a NUL byte → SkippedBinary (ditto)
//
// Does NOT print matched substrings or content — that would defeat
// the scrub. Returned paths are relative to `root`.
func ScanTreeForSecrets(root string) (ScanReport, error) {
	var report ScanReport
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		// Symlinks: SetupWorkspace already refused escaping ones + in-
		// tree ones were materialised as regular files. Anything still
		// tagged symlink here is defensive; skip AND do not upload.
		if info.Mode()&os.ModeSymlink != 0 {
			report.SkippedBinary = append(report.SkippedBinary, rel)
			return nil
		}
		if info.Size() > scanMaxFileSize {
			report.SkippedLarge = append(report.SkippedLarge, rel)
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		// Sniff for binary via NUL in the first N bytes.
		probe := make([]byte, scanBinaryProbeAdmission)
		n, _ := f.Read(probe)
		if bytes.Contains(probe[:n], []byte{0}) {
			report.SkippedBinary = append(report.SkippedBinary, rel)
			return nil
		}
		// Read whole file (bounded by scanMaxFileSize above).
		body := make([]byte, 0, info.Size())
		body = append(body, probe[:n]...)
		rest, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		body = append(body, rest...)

		// Detect secrets via chunked Sanitize. The naive `Sanitize(src)
		// != src` check fails on two axes:
		//   (1) Sanitize truncates output to 256 runes, so a secret past
		//       rune 256 has its `[REDACTED]` marker lopped off — the
		//       output looks like "prefix...[truncated]" with no
		//       `[REDACTED]`, and a file that IS leaky reads as safe.
		//   (2) A workload fixture that legitimately contains the
		//       string `[REDACTED]` (e.g. a doc explaining redaction)
		//       would false-positive a plain output-vs-input diff.
		//
		// Fix: split `src` into rune-bounded windows of scanChunkRunes
		// with an overlap of scanChunkOverlap so no single window ever
		// hits the truncation cap AND any secret straddling a boundary
		// still lands entirely inside at least one window. For each
		// window we compare `[REDACTED]` counts before/after — if any
		// window sees a marker Sanitize added, the file is flagged.
		if scanChunked(string(body)) {
			report.Flagged = append(report.Flagged, rel)
			return nil
		}
		report.SafeToUpload = append(report.SafeToUpload, rel)
		return nil
	})
	if err != nil {
		return ScanReport{}, fmt.Errorf("harness: scan walk %s: %w", root, err)
	}
	return report, nil
}
