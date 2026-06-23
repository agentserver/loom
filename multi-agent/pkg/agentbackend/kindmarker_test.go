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

func TestWireResultFromStoredOutputMatchesNormalPath(t *testing.T) {
	// Every case here pins what dispatch.Run's normal path would have
	// sent as the agentserver `result` field for the same stored output.
	// ack-drain and replay paths must produce the identical wire shape.
	cases := []struct {
		name       string
		stored     string
		wantRaw    bool
		wantString string // when wantRaw is false, expected unwrapped value
	}{
		{
			name:    "awaiting_user always raw",
			stored:  `{"kind":"awaiting_user","question":{"kind":"ask_user","question":"?"},"session_id":"thr-1"}`,
			wantRaw: true,
		},
		{
			name:    "final with session_id raw",
			stored:  `{"kind":"final","summary":"x","session_id":"thr-1"}`,
			wantRaw: true,
		},
		{
			// THIS is the case the round-5 review caught. Run would have
			// sent res.Summary = "ok" plain; ack/replay MUST do the same.
			name:       "final with empty session_id unwraps to summary",
			stored:     `{"kind":"final","summary":"ok","session_id":""}`,
			wantRaw:    false,
			wantString: "ok",
		},
		{
			name:       "final with empty session_id and empty summary unwraps to empty",
			stored:     `{"kind":"final","summary":"","session_id":""}`,
			wantRaw:    false,
			wantString: "",
		},
		{
			name:       "missing session_id key treated as empty",
			stored:     `{"kind":"final","summary":"hello"}`,
			wantRaw:    false,
			wantString: "hello",
		},
		{
			name:       "non-envelope raw string passes through",
			stored:     "raw bash output",
			wantRaw:    false,
			wantString: "raw bash output",
		},
		{
			name:       "non-envelope JSON passes through verbatim (string-encoded by caller)",
			stored:     `{"ok":true}`,
			wantRaw:    false,
			wantString: `{"ok":true}`,
		},
		{
			name:       "empty input passes through",
			stored:     "",
			wantRaw:    false,
			wantString: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRaw, gotPayload := WireResultFromStoredOutput(tc.stored)
			if gotRaw != tc.wantRaw {
				t.Errorf("raw=%v, want %v", gotRaw, tc.wantRaw)
			}
			if !tc.wantRaw && gotPayload != tc.wantString {
				t.Errorf("payload=%q, want %q", gotPayload, tc.wantString)
			}
			if tc.wantRaw && gotPayload != tc.stored {
				t.Errorf("raw payload=%q, want stored verbatim %q", gotPayload, tc.stored)
			}
		})
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
