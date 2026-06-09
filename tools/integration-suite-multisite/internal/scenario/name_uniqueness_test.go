package scenario

import (
	"strings"
	"testing"
)

func TestCheckScenarioNameUniqueness_CleanSet(t *testing.T) {
	scs := []*Scenario{
		{Name: "alpha", SourcePath: "/d/alpha.yaml"},
		{Name: "beta", SourcePath: "/d/beta.yaml"},
		{Name: "gamma", SourcePath: "/d/sub/gamma.yaml"},
	}
	if errs := CheckScenarioNameUniqueness(scs); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestCheckScenarioNameUniqueness_DuplicateAcrossSubdirs(t *testing.T) {
	scs := []*Scenario{
		{Name: "Room Creates", SourcePath: "/d/messages/room-creates.yaml"},
		{Name: "Room Creates", SourcePath: "/d/rooms/room-creates.yaml"},
	}
	errs := CheckScenarioNameUniqueness(scs)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	for _, want := range []string{"Room Creates", "messages/room-creates.yaml", "rooms/room-creates.yaml", "perf-history key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestCheckScenarioNameUniqueness_MultipleDuplicates_DeterministicOrder(t *testing.T) {
	scs := []*Scenario{
		{Name: "B-dup", SourcePath: "/d/b1.yaml"},
		{Name: "B-dup", SourcePath: "/d/b2.yaml"},
		{Name: "A-dup", SourcePath: "/d/a2.yaml"},
		{Name: "A-dup", SourcePath: "/d/a1.yaml"},
	}
	errs := CheckScenarioNameUniqueness(scs)
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), `"A-dup"`) {
		t.Errorf("expected first error for A-dup, got: %s", errs[0])
	}
	if !strings.Contains(errs[1].Error(), `"B-dup"`) {
		t.Errorf("expected second error for B-dup, got: %s", errs[1])
	}
	if !strings.Contains(errs[0].Error(), "[/d/a1.yaml /d/a2.yaml]") {
		t.Errorf("expected paths in A-dup error to be sorted, got: %s", errs[0])
	}
}

func TestCheckScenarioNameUniqueness_SkipsEmptyName(t *testing.T) {
	scs := []*Scenario{
		{Name: "", SourcePath: "/d/no-name-1.yaml"},
		{Name: "", SourcePath: "/d/no-name-2.yaml"},
	}
	if errs := CheckScenarioNameUniqueness(scs); len(errs) != 0 {
		t.Fatalf("expected empty-name entries to be skipped, got %v", errs)
	}
}

func TestCheckScenarioNameUniqueness_NilSafe(t *testing.T) {
	scs := []*Scenario{
		nil,
		{Name: "alpha", SourcePath: "/d/alpha.yaml"},
	}
	if errs := CheckScenarioNameUniqueness(scs); len(errs) != 0 {
		t.Fatalf("expected nil entry to be skipped, got %v", errs)
	}
}
