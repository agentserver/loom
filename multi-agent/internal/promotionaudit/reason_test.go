package promotionaudit

import (
	"errors"
	"strings"
	"testing"
)

func TestReason_ParseAcceptsAllFour(t *testing.T) {
	cases := []Reason{
		ReasonExplicitUserRequest,
		ReasonDriverAgentInferred,
		ReasonBatchImport,
		ReasonCISeed,
	}
	for _, want := range cases {
		got, err := Parse(string(want))
		if err != nil {
			t.Errorf("Parse(%q) unexpected err: %v", want, err)
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestReason_ParseRejectsCaseVariant(t *testing.T) {
	if _, err := Parse("Explicit_User_Request"); !errors.Is(err, ErrInvalidPromotionReason) {
		t.Fatalf("expected ErrInvalidPromotionReason, got %v", err)
	}
}

func TestReason_ParseRejectsWhitespace(t *testing.T) {
	for _, s := range []string{" explicit_user_request", "explicit_user_request ", "explicit_user_request\n"} {
		if _, err := Parse(s); !errors.Is(err, ErrInvalidPromotionReason) {
			t.Errorf("Parse(%q) expected reject, got err=%v", s, err)
		}
	}
}

func TestReason_ParseRejectsEmpty(t *testing.T) {
	if _, err := Parse(""); !errors.Is(err, ErrInvalidPromotionReason) {
		t.Fatalf("expected reject for empty, got %v", err)
	}
}

func TestReason_ParseRejectsUnknown(t *testing.T) {
	if _, err := Parse("foo"); !errors.Is(err, ErrInvalidPromotionReason) {
		t.Fatalf("expected reject for 'foo', got %v", err)
	}
}

func TestReason_ParseRejectsUTF8Lookalike(t *testing.T) {
	// Cyrillic 'а' (U+0430) in place of ASCII 'a' in "batch_import".
	input := "bаtch_import"
	if input == string(ReasonBatchImport) {
		t.Fatalf("test setup wrong: input already matches canonical byte-for-byte")
	}
	if _, err := Parse(input); !errors.Is(err, ErrInvalidPromotionReason) {
		t.Fatalf("Parse(%q) should reject UTF-8 lookalike, got %v", input, err)
	}
}

// TestReason_ErrorMessageDoesNotEchoInput enforces spec §7 (f): the
// error message must NOT contain the raw caller input, so a secret in
// the input cannot leak through the error path.
func TestReason_ErrorMessageDoesNotEchoInput(t *testing.T) {
	secret := "sk-live-1234567890abcdef"
	_, err := Parse(secret)
	if err == nil {
		t.Fatal("expected reject")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message %q echoes raw input %q — see §7 (f)", err.Error(), secret)
	}
	// Check the fixed message shape.
	want := "promotion_reason must be one of:"
	if !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error prefix mismatch: got %q want prefix %q", err.Error(), want)
	}
}

func TestReason_StringMatchesLiteral(t *testing.T) {
	got := ReasonExplicitUserRequest.String()
	want := "explicit_user_request"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
