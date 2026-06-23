package agentbackend

import "testing"

func TestUnwrapKindMarkerFinal(t *testing.T) {
	a, sum, q := UnwrapKindMarker(`{"kind":"final","summary":"hello","session_id":"thr-1"}`)
	if a {
		t.Fatal("final envelope must not report awaiting")
	}
	if sum != "hello" {
		t.Fatalf("summary = %q, want hello", sum)
	}
	if q != nil {
		t.Fatalf("question must be nil for final: %s", string(q))
	}
}

func TestUnwrapKindMarkerAwaiting(t *testing.T) {
	a, sum, q := UnwrapKindMarker(`{"kind":"awaiting_user","question":{"kind":"ask_user","question":"?"},"session_id":"thr-2"}`)
	if !a {
		t.Fatal("awaiting envelope must report awaiting=true")
	}
	if sum != "" {
		t.Fatalf("awaiting must have empty summary, got %q", sum)
	}
	if len(q) == 0 {
		t.Fatal("question payload must be carried through")
	}
}

func TestUnwrapKindMarkerNotAnEnvelope(t *testing.T) {
	// Non-envelope inputs must return all zero values so callers fall back
	// to the raw text. Crucially, valid JSON that ISN'T an envelope (e.g.
	// bash output that happens to be a JSON object) must NOT be treated
	// as an envelope.
	cases := []string{
		"",
		"raw text",
		`{"ok":true}`,
		`{"output":"plain bash json"}`,
		`[1,2,3]`,
		`"just a string"`,
		`{"kind":"something_else","summary":"x"}`,
	}
	for _, s := range cases {
		a, sum, q := UnwrapKindMarker(s)
		if a || sum != "" || q != nil {
			t.Fatalf("input %q must not parse as envelope, got (%v, %q, %s)", s, a, sum, string(q))
		}
	}
}

func TestIsKindMarkerEnvelopeOnlyMatchesKnownKinds(t *testing.T) {
	yes := []string{
		`{"kind":"final","summary":"x","session_id":"s"}`,
		`{"kind":"final","summary":"","session_id":""}`, // empty-summary final still IS an envelope
		`{"kind":"awaiting_user","question":{},"session_id":"s"}`,
	}
	no := []string{
		"",
		"raw bash output",
		`{"ok":true}`,
		`{"output":"plain bash json"}`,
		`[1,2,3]`,
		`"just a string"`,
		`{"kind":"something_else"}`,
		`{"summary":"missing kind"}`,
	}
	for _, s := range yes {
		if !IsKindMarkerEnvelope(s) {
			t.Errorf("IsKindMarkerEnvelope(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsKindMarkerEnvelope(s) {
			t.Errorf("IsKindMarkerEnvelope(%q) = true, want false (would corrupt non-chat JSON outputs)", s)
		}
	}
}
