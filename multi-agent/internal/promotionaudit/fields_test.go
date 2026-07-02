package promotionaudit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// validRow returns a canonical AuditFields that Validate accepts.
func validRow() AuditFields {
	return AuditFields{
		WorkspaceID:           "ws-abc12345",
		MCPName:               "mytool",
		Action:                ActionRegister,
		PromotedByUserID:      "user_abcdef",
		DriverThreadID:        "thread_01_a",
		PromotionReason:       ReasonExplicitUserRequest,
		CandidateSourceTaskID: "task_12345678",
		RegistryHashAfter:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Stage:                 "",
		StageResult:           StageResultEmpty,
		TS:                    time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}
}

func TestAuditFields_ValidateAcceptsCanonical(t *testing.T) {
	if err := Validate(validRow()); err != nil {
		t.Fatalf("canonical row rejected: %v", err)
	}
}

func TestAuditFields_RejectsMissingUserID(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = ""
	err := Validate(r)
	if !errors.Is(err, ErrMissingField) {
		t.Fatalf("want ErrMissingField, got %v", err)
	}
	if !strings.Contains(err.Error(), "promoted_by_user_id") {
		t.Errorf("error should name field: %v", err)
	}
}

func TestAuditFields_RejectsMissingThreadID(t *testing.T) {
	r := validRow()
	r.DriverThreadID = ""
	if err := Validate(r); !errors.Is(err, ErrMissingField) {
		t.Fatalf("want ErrMissingField, got %v", err)
	}
}

func TestAuditFields_RejectsMissingCandidateTaskID(t *testing.T) {
	r := validRow()
	r.CandidateSourceTaskID = ""
	if err := Validate(r); !errors.Is(err, ErrMissingJoinKey) {
		t.Fatalf("want ErrMissingJoinKey, got %v", err)
	}
}

func TestAuditFields_RejectsShortUserID(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "usr1234" // 7 chars, 1 below bound
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_RejectsLongUserID(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = strings.Repeat("a", 129)
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_RejectsUserIDWithSlash(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "abc/../x1"
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_RejectsUserIDWithQuote(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "abc'--xx"
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_RejectsUserIDWithLikeWildcard(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "ab%_xxxx"
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_RejectsUserIDWithSpace(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "abc defg"
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat, got %v", err)
	}
}

func TestAuditFields_UserIDNotEmailShape(t *testing.T) {
	r := validRow()
	r.PromotedByUserID = "x@y.zzz01" // contains @ and .
	if err := Validate(r); !errors.Is(err, ErrFieldFormat) {
		t.Fatalf("want ErrFieldFormat for email-shape, got %v", err)
	}
}

func TestAuditFields_RejectsUnknownAction(t *testing.T) {
	r := validRow()
	r.Action = Action("bogus")
	if err := Validate(r); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("want ErrUnknownAction, got %v", err)
	}
}

func TestAuditFields_RejectsUnknownReason(t *testing.T) {
	r := validRow()
	r.PromotionReason = Reason("garbage")
	err := Validate(r)
	if !errors.Is(err, ErrInvalidPromotionReason) {
		t.Fatalf("want ErrInvalidPromotionReason, got %v", err)
	}
}

func TestAuditFields_AcceptsEmptyWorkspaceIDForInstall(t *testing.T) {
	r := validRow()
	r.Action = ActionInstall
	r.WorkspaceID = ""
	r.RegistryHashAfter = "" // install offline has no registry effect
	if err := Validate(r); err != nil {
		t.Fatalf("install with empty workspace should be accepted, got %v", err)
	}
}

func TestAuditFields_RejectsEmptyWorkspaceIDForRegister(t *testing.T) {
	r := validRow()
	r.Action = ActionRegister
	r.WorkspaceID = ""
	if err := Validate(r); !errors.Is(err, ErrInvalidWorkspaceID) {
		t.Fatalf("want ErrInvalidWorkspaceID, got %v", err)
	}
}

func TestAuditFields_RegistryHashAfterFormat(t *testing.T) {
	r := validRow()
	r.RegistryHashAfter = "not-hex"
	if err := Validate(r); !errors.Is(err, ErrInvalidRegistryHash) {
		t.Fatalf("want ErrInvalidRegistryHash for non-hex, got %v", err)
	}
	// Wrong length.
	r.RegistryHashAfter = strings.Repeat("a", 63)
	if err := Validate(r); !errors.Is(err, ErrInvalidRegistryHash) {
		t.Fatalf("want ErrInvalidRegistryHash for wrong length, got %v", err)
	}
	// Uppercase hex — rejected (registryHashRE uses lowercase).
	r.RegistryHashAfter = strings.Repeat("A", 64)
	if err := Validate(r); !errors.Is(err, ErrInvalidRegistryHash) {
		t.Fatalf("want ErrInvalidRegistryHash for uppercase hex, got %v", err)
	}
	// Empty for register — rejected.
	r = validRow()
	r.RegistryHashAfter = ""
	r.Action = ActionRegister
	if err := Validate(r); !errors.Is(err, ErrInvalidRegistryHash) {
		t.Fatalf("want ErrInvalidRegistryHash for empty hash on register, got %v", err)
	}
}

func TestAuditFields_StageMustBeLowercaseOrEmpty(t *testing.T) {
	r := validRow()
	r.Stage = "" // canonical
	if err := Validate(r); err != nil {
		t.Fatalf("empty stage rejected: %v", err)
	}
	r.Stage = "Scaffold"
	if err := Validate(r); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("uppercase stage should reject, got %v", err)
	}
	r.Stage = "scaffold"
	if err := Validate(r); err != nil {
		t.Fatalf("lowercase stage should accept, got %v", err)
	}
	r.Stage = strings.Repeat("a", 65)
	if err := Validate(r); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("65-char stage should reject, got %v", err)
	}
}

func TestAuditFields_ConsumerViewJoinKeyPresent(t *testing.T) {
	// Empty candidate task id → reject.
	r := validRow()
	r.CandidateSourceTaskID = ""
	if err := Validate(r); !errors.Is(err, ErrMissingJoinKey) {
		t.Fatalf("want ErrMissingJoinKey, got %v", err)
	}

	// ci_seed sentinel + ci_seed reason → accept.
	r = validRow()
	r.PromotionReason = ReasonCISeed
	sentinel, err := SentinelCandidateTaskID(ReasonCISeed, r.WorkspaceID)
	if err != nil {
		t.Fatalf("sentinel build: %v", err)
	}
	r.CandidateSourceTaskID = sentinel
	if err := Validate(r); err != nil {
		t.Fatalf("ci_seed sentinel+reason rejected: %v", err)
	}

	// ci_seed sentinel + explicit_user_request reason → reject (invariant).
	r.PromotionReason = ReasonExplicitUserRequest
	if err := Validate(r); !errors.Is(err, ErrSentinelReasonMismatch) {
		t.Fatalf("want ErrSentinelReasonMismatch, got %v", err)
	}

	// batch_import reason + non-sentinel candidate id → reject (invariant reverse).
	r = validRow()
	r.PromotionReason = ReasonBatchImport
	r.CandidateSourceTaskID = "task_abcdefgh"
	if err := Validate(r); !errors.Is(err, ErrSentinelReasonMismatch) {
		t.Fatalf("want ErrSentinelReasonMismatch reverse, got %v", err)
	}
}

func TestSentinelCandidateTaskID_MatchesRegex(t *testing.T) {
	for _, tc := range []struct {
		reason      Reason
		workspaceID string
	}{
		{ReasonCISeed, "ws-abc12345"},
		{ReasonBatchImport, "ws-1"}, // short workspace → padded to 8
	} {
		s, err := SentinelCandidateTaskID(tc.reason, tc.workspaceID)
		if err != nil {
			t.Fatalf("build sentinel %+v: %v", tc, err)
		}
		if !candIDRE.MatchString(s) {
			t.Errorf("sentinel %q for %+v does not match candIDRE", s, tc)
		}
	}
	// Refuses non-sentinel reasons.
	if _, err := SentinelCandidateTaskID(ReasonExplicitUserRequest, "ws-abc12345"); !errors.Is(err, ErrSentinelReasonMismatch) {
		t.Fatalf("want ErrSentinelReasonMismatch, got %v", err)
	}
	// Refuses empty workspace.
	if _, err := SentinelCandidateTaskID(ReasonCISeed, ""); !errors.Is(err, ErrMissingField) {
		t.Fatalf("want ErrMissingField, got %v", err)
	}
}
