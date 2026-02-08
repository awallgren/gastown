package witness

import (
	"testing"
)

func TestZombieResult_Types(t *testing.T) {
	// Verify the ZombieResult type has all expected fields
	z := ZombieResult{
		PolecatName: "nux",
		AgentState:  "working",
		HookBead:    "gt-abc123",
		Action:      "auto-nuked",
		Error:       nil,
	}

	if z.PolecatName != "nux" {
		t.Errorf("PolecatName = %q, want %q", z.PolecatName, "nux")
	}
	if z.AgentState != "working" {
		t.Errorf("AgentState = %q, want %q", z.AgentState, "working")
	}
	if z.HookBead != "gt-abc123" {
		t.Errorf("HookBead = %q, want %q", z.HookBead, "gt-abc123")
	}
	if z.Action != "auto-nuked" {
		t.Errorf("Action = %q, want %q", z.Action, "auto-nuked")
	}
}

func TestDetectZombiePolecatsResult_EmptyResult(t *testing.T) {
	result := &DetectZombiePolecatsResult{}

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies length = %d, want 0", len(result.Zombies))
	}
}

func TestDetectZombiePolecats_NonexistentDir(t *testing.T) {
	// Should handle missing polecats directory gracefully
	result := DetectZombiePolecats("/nonexistent/path", "testrig", nil)

	if result.Checked != 0 {
		t.Errorf("Checked = %d, want 0 for nonexistent dir", result.Checked)
	}
	if len(result.Zombies) != 0 {
		t.Errorf("Zombies = %d, want 0 for nonexistent dir", len(result.Zombies))
	}
}

func TestGetAgentBeadState_EmptyOutput(t *testing.T) {
	// getAgentBeadState with invalid bead ID should return empty strings
	// (it calls bd which won't exist in test, so it returns empty)
	state, hook := getAgentBeadState("/nonexistent", "nonexistent-bead")

	if state != "" {
		t.Errorf("state = %q, want empty for missing bead", state)
	}
	if hook != "" {
		t.Errorf("hook = %q, want empty for missing bead", hook)
	}
}
