package main

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCSVColumns_FrozenOrder pins the column order at the byte level so a
// future refactor that "just" alphabetises the list trips this test. The
// downstream run-schema worktree and the metric-extractor scripts rely on
// this order; an append-only schema policy means new columns go at the end.
func TestCSVColumns_FrozenOrder(t *testing.T) {
	t.Parallel()
	got := strings.Join(CSVColumns(), ",")
	want := strings.Join([]string{
		"run_id", "workload_id", "started_at_unix", "finished_at_unix",
		"duration_ms", "passed", "oracle_exit_code", "oracle_details_json",
		"oracle_metrics_json", "loom_commit", "agentserver_commit",
		"modelserver_commit", "app_commit", "os_kernel", "os_distro",
		"os_arch", "machine_hostname", "author_email_sha8",
		"committer_email_sha8", "codex_config_path", "stub_listen",
		"tempdir_kept",
	}, ",")
	if got != want {
		t.Fatalf("CSV columns drifted:\n got  %s\n want %s", got, want)
	}
}

// TestWriteCSVRow_HeaderPlusOneDataRow — the acceptance smoke counts lines,
// so writing produces exactly two lines (header + 1 data row + trailing
// newline-terminated == 2 record lines for wc -l).
func TestWriteCSVRow_HeaderPlusOneDataRow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.csv")
	row := RunRow{
		RunID:           "run-xyz",
		WorkloadID:      "cross-device-code-mod",
		Passed:          true,
		OracleExitCode:  0,
		LoomCommit:      "abc1234 (master clean)",
		AuthorEmailSHA8: "deadbeef",
		StubListen:      "127.0.0.1:18080",
	}
	if err := WriteCSVRow(path, row); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (header+row)", len(records))
	}
	if records[0][0] != "run_id" {
		t.Fatalf("first record is not header: %v", records[0])
	}
	if records[1][0] != "run-xyz" || records[1][5] != "true" {
		t.Fatalf("data row drift: %v", records[1])
	}
}

// TestCSVRow_AppendForbidden — running twice with the same --out must fail
// loudly rather than silently grow the file or rewrite the header line.
// Defends §3 of the spec ("no accidental append").
func TestCSVRow_AppendForbidden(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run.csv")
	row := RunRow{RunID: "first"}
	if err := WriteCSVRow(path, row); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := WriteCSVRow(path, RunRow{RunID: "second"})
	if !errors.Is(err, ErrCSVExists) {
		t.Fatalf("second write err = %v, want ErrCSVExists", err)
	}
}

// TestWriteCSVRow_EmptyPathRejected guards against `--out=""` falling
// through to a current-directory write.
func TestWriteCSVRow_EmptyPathRejected(t *testing.T) {
	t.Parallel()
	if err := WriteCSVRow("", RunRow{}); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestNoopWriter_InsertIsNoop confirms the placeholder writer satisfies the
// interface and doesn't error. This test exists so a future "make Noop log
// to stderr" change has to update its assertion explicitly.
func TestNoopWriter_InsertIsNoop(t *testing.T) {
	t.Parallel()
	var w RunWriter = NoopWriter{}
	if err := w.Insert(context.Background(), RunRow{RunID: "x"}); err != nil {
		t.Fatalf("noop insert: %v", err)
	}
}
