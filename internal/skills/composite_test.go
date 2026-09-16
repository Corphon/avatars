package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompositeStore_OverlaySkillsAppearInApprovedListing(t *testing.T) {
	tempDir := t.TempDir()
	primary := NewStore(filepath.Join(tempDir, "skills"))
	if err := os.MkdirAll(filepath.Join(tempDir, "skills", "approved"), 0o755); err != nil {
		t.Fatalf("mkdir approved failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "skills", "approved", "base.md"), []byte(`---
name: base
description: base skill
when_to_use: use base
---
base
`), 0o644); err != nil {
		t.Fatalf("write base skill failed: %v", err)
	}
	overlay := []Definition{{
		Listing: Listing{
			Name:           "caveman:caveman",
			Description:    "plugin skill",
			WhenToUse:      "use terse output",
			LifecycleState: "plugin",
			Path:           filepath.Join("caveman", "skills", "caveman", "SKILL.md"),
		},
	}}
	composite := NewCompositeStore(primary, overlay)
	listings, err := composite.ListApproved()
	if err != nil {
		t.Fatalf("list approved failed: %v", err)
	}
	if len(listings) != 2 {
		t.Fatalf("expected 2 listings, got %d", len(listings))
	}
	definition, err := composite.LoadApproved(filepath.Join("caveman", "skills", "caveman", "SKILL.md"))
	if err != nil {
		t.Fatalf("load overlay skill failed: %v", err)
	}
	if definition.Name != "caveman:caveman" {
		t.Fatalf("expected overlay skill name, got %q", definition.Name)
	}
}
