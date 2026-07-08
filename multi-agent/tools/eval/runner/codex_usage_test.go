package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeCodexUsageJSONL(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-usage.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write usage jsonl: %v", err)
	}
	return path
}

func TestParseCodexUsageJSONL_SumsUsageRecords(t *testing.T) {
	t.Parallel()
	path := writeCodexUsageJSONL(t, `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}
{"type":"debug","message":"ignored"}
{"type":"turn.completed","message":{"usage":{"prompt_tokens":2,"completion_tokens":1}}}
{"type":"response.completed","response":{"usage":{"input_tokens":3,"completion_tokens":5}}}
`)

	got, err := ParseCodexUsageJSONL(path)
	if err != nil {
		t.Fatalf("ParseCodexUsageJSONL: %v", err)
	}
	if got.InputTokens != 15 {
		t.Fatalf("input tokens = %d, want 15", got.InputTokens)
	}
	if got.OutputTokens != 10 {
		t.Fatalf("output tokens = %d, want 10", got.OutputTokens)
	}
}

func TestParseCodexUsageJSONL_EmptyPathReturnsZero(t *testing.T) {
	t.Parallel()
	got, err := ParseCodexUsageJSONL("")
	if err != nil {
		t.Fatalf("empty path should be a no-op, got %v", err)
	}
	if got != (CodexTokenUsage{}) {
		t.Fatalf("empty path usage = %+v, want zero value", got)
	}
}

func TestParseCodexUsageJSONL_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	path := writeCodexUsageJSONL(t, "{\"usage\":{\"input_tokens\":1}\n")

	_, err := ParseCodexUsageJSONL(path)
	if !errors.Is(err, ErrCodexUsageJSONLInvalid) {
		t.Fatalf("err = %v, want ErrCodexUsageJSONLInvalid", err)
	}
}

func TestParseCodexUsageJSONL_RejectsNegativeTokens(t *testing.T) {
	t.Parallel()
	path := writeCodexUsageJSONL(t, `{"usage":{"input_tokens":-1,"output_tokens":0}}`+"\n")

	_, err := ParseCodexUsageJSONL(path)
	if !errors.Is(err, ErrCodexUsageJSONLInvalid) {
		t.Fatalf("err = %v, want ErrCodexUsageJSONLInvalid", err)
	}
}

func TestParseCodexUsageJSONL_UnreadablePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.jsonl")

	_, err := ParseCodexUsageJSONL(path)
	if !errors.Is(err, ErrCodexUsageJSONLUnreadable) {
		t.Fatalf("err = %v, want ErrCodexUsageJSONLUnreadable", err)
	}
}
