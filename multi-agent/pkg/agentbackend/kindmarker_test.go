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

func TestShouldForwardEnvelopeRawMatchesNormalPathContract(t *testing.T) {
	// Mirror the normal-path WrappedOutput gate in dispatch.Run:
	//   res.AwaitingUser != nil || res.SessionID != ""
	// On ack/replay paths we only have the stored envelope string, so the
	// equivalent shape predicate is:
	//   kind == "awaiting_user"  OR  (kind == "final" && session_id != "")
	yes := []string{
		// awaiting_user: always raw-forward (driver needs the question payload).
		`{"kind":"awaiting_user","question":{},"session_id":"s"}`,
		`{"kind":"awaiting_user","question":{}}`,
		// final + session_id: reverse parent link needs the session id.
		`{"kind":"final","summary":"x","session_id":"thr-1"}`,
		`{"kind":"final","summary":"","session_id":"thr-1"}`,
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
		// final + empty session_id: downstream gets nothing useful from the
		// envelope shape. Normal path sends raw "ok"; ack/replay MUST match.
		// This is the r4-#1 asymmetry guard — see review notes.
		`{"kind":"final","summary":"ok","session_id":""}`,
		`{"kind":"final","summary":"ok"}`, // missing session_id key = same case
	}
	for _, s := range yes {
		if !ShouldForwardEnvelopeRaw(s) {
			t.Errorf("ShouldForwardEnvelopeRaw(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if ShouldForwardEnvelopeRaw(s) {
			t.Errorf("ShouldForwardEnvelopeRaw(%q) = true, want false (would create wire-shape skew vs normal path)", s)
		}
	}
}
