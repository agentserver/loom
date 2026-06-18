package executor

import "testing"

func TestTaskParentFields(t *testing.T) {
	task := Task{
		ParentSessionID:   "parent-thread",
		ParentAgentID:     "drv-abc",
		ParentDisplayName: "prod-driver",
	}
	if task.ParentSessionID != "parent-thread" {
		t.Fatalf("ParentSessionID = %q, want parent-thread", task.ParentSessionID)
	}
	if task.ParentAgentID != "drv-abc" {
		t.Fatalf("ParentAgentID = %q, want drv-abc", task.ParentAgentID)
	}
	if task.ParentDisplayName != "prod-driver" {
		t.Fatalf("ParentDisplayName = %q, want prod-driver", task.ParentDisplayName)
	}
}
