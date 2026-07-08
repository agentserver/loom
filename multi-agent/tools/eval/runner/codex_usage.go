package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrCodexUsageJSONLUnreadable = errors.New("eval-runner: --codex-usage-jsonl could not be read")
	ErrCodexUsageJSONLInvalid    = errors.New("eval-runner: --codex-usage-jsonl invalid")
)

type CodexTokenUsage struct {
	InputTokens  int
	OutputTokens int
}

func ParseCodexUsageJSONL(path string) (CodexTokenUsage, error) {
	if path == "" {
		return CodexTokenUsage{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return CodexTokenUsage{}, fmt.Errorf("%w: %s: %v", ErrCodexUsageJSONLUnreadable, path, err)
	}
	defer f.Close()

	var total CodexTokenUsage
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return CodexTokenUsage{}, fmt.Errorf("%w: line %d: %v", ErrCodexUsageJSONLInvalid, lineNo, err)
		}
		u, ok, err := codexUsageFromObject(obj)
		if err != nil {
			return CodexTokenUsage{}, fmt.Errorf("%w: line %d: %v", ErrCodexUsageJSONLInvalid, lineNo, err)
		}
		if !ok {
			continue
		}
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
	}
	if err := sc.Err(); err != nil {
		return CodexTokenUsage{}, fmt.Errorf("%w: %s: %v", ErrCodexUsageJSONLUnreadable, path, err)
	}
	return total, nil
}

func codexUsageFromObject(obj map[string]any) (CodexTokenUsage, bool, error) {
	if usage, ok := mapValue(obj["usage"]); ok {
		u, err := codexUsageFromMap(usage)
		return u, true, err
	}
	for _, key := range []string{"event", "response", "message"} {
		wrapper, ok := mapValue(obj[key])
		if !ok {
			continue
		}
		if usage, ok := mapValue(wrapper["usage"]); ok {
			u, err := codexUsageFromMap(usage)
			return u, true, err
		}
	}
	return CodexTokenUsage{}, false, nil
}

func codexUsageFromMap(m map[string]any) (CodexTokenUsage, error) {
	input, err := firstTokenValue(m, "input_tokens", "prompt_tokens")
	if err != nil {
		return CodexTokenUsage{}, err
	}
	output, err := firstTokenValue(m, "output_tokens", "completion_tokens")
	if err != nil {
		return CodexTokenUsage{}, err
	}
	return CodexTokenUsage{InputTokens: input, OutputTokens: output}, nil
}

func firstTokenValue(m map[string]any, keys ...string) (int, error) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		n, err := tokenInt(raw)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return n, nil
	}
	return 0, nil
}

func tokenInt(v any) (int, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("token count is %T, want number", v)
	}
	if f < 0 {
		return 0, errors.New("token count must be non-negative")
	}
	if f != float64(int(f)) {
		return 0, errors.New("token count must be an integer")
	}
	return int(f), nil
}

func mapValue(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}
