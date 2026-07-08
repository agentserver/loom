package harness

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateBaselineName_ValidValues(t *testing.T) {
	valid := []string{
		"manual_ssh",
		"single_machine_codex",
		"cloud_sandbox_e2b",
		"abc",                         // minimum length 3
		"x-y-z_1",                     // mixed separators
		"a" + strings.Repeat("b", 63), // maximum length 64
	}
	for _, v := range valid {
		if err := ValidateBaselineName(v); err != nil {
			t.Errorf("ValidateBaselineName(%q) unexpected error: %v", v, err)
		}
	}
}

func TestValidateBaselineName_RejectsSQLInjection(t *testing.T) {
	// Spec §7(d): the row value ends up in shell scripts and CSV
	// exports; a shell-escaping value is a chain of pain even though
	// SQL params defend at the DB layer.
	bad := "foo'; DROP TABLE runs; --"
	err := ValidateBaselineName(bad)
	if err == nil {
		t.Fatalf("expected error for SQL-injection-shaped input, got nil")
	}
	if !errors.Is(err, ErrBaselineNameInvalid) {
		t.Errorf("expected ErrBaselineNameInvalid, got %v", err)
	}
	// Error must include the rejected value (aids debugging) — but the
	// value is echoed by wrapping, not by string concat that could hide
	// the fact of a rejection.
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error should echo the rejected value, got %q", err.Error())
	}
}

func TestValidateBaselineName_RejectsLeadingDigit(t *testing.T) {
	// Leading `[a-z]` requirement — the regex-as-tag needs to be a
	// valid identifier prefix.
	if err := ValidateBaselineName("1abc"); err == nil {
		t.Errorf("expected leading-digit input to be rejected")
	}
}

func TestValidateBaselineName_RejectsTooShort(t *testing.T) {
	for _, v := range []string{"", "a", "ab"} {
		if err := ValidateBaselineName(v); err == nil {
			t.Errorf("expected too-short input %q to be rejected", v)
		}
	}
}

func TestValidateBaselineName_RejectsTooLong(t *testing.T) {
	long := "a" + strings.Repeat("b", 64) // 65 runes total
	if err := ValidateBaselineName(long); err == nil {
		t.Errorf("expected 65-rune input to be rejected")
	}
}

func TestValidateBaselineName_RejectsUpperAndPunct(t *testing.T) {
	bad := []string{
		"Manual_SSH",    // uppercase
		"manual ssh",    // space
		"manual.ssh",    // dot
		"manual/ssh",    // slash
		"foo\"; rm -rf", // quote + shell metachars
		"foo\tbar",      // tab
	}
	for _, v := range bad {
		if err := ValidateBaselineName(v); err == nil {
			t.Errorf("expected %q to be rejected", v)
		}
	}
}
