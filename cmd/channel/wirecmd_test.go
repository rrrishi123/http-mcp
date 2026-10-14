package main

import (
	"encoding/json"
	"testing"
)

// TestWireCmd guards the B4 sessionId passthrough: the frame carries sessionId
// only when it is non-empty (flat-mode routing to an attached target), and the
// id/method/params are always present and correct.
func TestWireCmd(t *testing.T) {
	params := json.RawMessage(`{"format":"jpeg"}`)

	// with a sessionId -> it must appear at the top level (flat-mode routing)
	var withS map[string]any
	if err := json.Unmarshal(wireCmd(7, "Page.captureScreenshot", params, "S1"), &withS); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if withS["sessionId"] != "S1" {
		t.Errorf("sessionId not forwarded: %v", withS["sessionId"])
	}
	if withS["method"] != "Page.captureScreenshot" || withS["id"] != float64(7) {
		t.Errorf("id/method wrong: %v", withS)
	}

	// without a sessionId -> the key must be ABSENT (not "" — that would address
	// a real, empty session and misroute)
	var noS map[string]any
	json.Unmarshal(wireCmd(8, "Target.getTargets", nil, ""), &noS)
	if _, present := noS["sessionId"]; present {
		t.Errorf("sessionId must be absent when empty, got: %v", noS["sessionId"])
	}
	if noS["id"] != float64(8) || noS["method"] != "Target.getTargets" {
		t.Errorf("id/method wrong: %v", noS)
	}
}
