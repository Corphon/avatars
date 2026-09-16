package workflow

import "testing"

func TestIsTemplatePlaceholderLabel_PhaseScope(t *testing.T) {
	cases := []string{
		"Phase 2 scope",
		"- [ ] 1. Phase 2 scope (0/1)",
		"Expand phase detail and implement",
		"[Task Group Name]",
	}
	for _, c := range cases {
		if !isTemplatePlaceholderLabel(c) {
			t.Fatalf("expected placeholder: %q", c)
		}
	}
	if isTemplatePlaceholderLabel("Implement API-Key middleware") {
		t.Fatal("real task must not be placeholder")
	}
}
