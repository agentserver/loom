package contract

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	EnvelopeStart = "<TASK_CONTRACT version=1>"
	EnvelopeEnd   = "</TASK_CONTRACT>"
)

func EncodeEnvelope(tc TaskContract, body string) (string, error) {
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		return "", err
	}
	return EnvelopeStart + "\n" + string(b) + "\n" + EnvelopeEnd + "\n\n" + strings.TrimSpace(body), nil
}

func DecodeEnvelope(prompt string) (TaskContract, string, bool, error) {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, EnvelopeStart) {
		return TaskContract{}, prompt, false, nil
	}
	rest := strings.TrimPrefix(trimmed, EnvelopeStart)
	idx := strings.Index(rest, EnvelopeEnd)
	if idx < 0 {
		return TaskContract{}, "", true, fmt.Errorf("task contract envelope missing end marker")
	}
	raw := strings.TrimSpace(rest[:idx])
	body := strings.TrimSpace(rest[idx+len(EnvelopeEnd):])
	var tc TaskContract
	if err := json.Unmarshal([]byte(raw), &tc); err != nil {
		return TaskContract{}, "", true, fmt.Errorf("decode task contract: %w", err)
	}
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		return TaskContract{}, "", true, err
	}
	return tc, body, true, nil
}
