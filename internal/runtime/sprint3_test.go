package runtime

import (
	"testing"

	"avatars/internal/skills"
)

func TestS3_FilterAlwaysOnSkills_ByRoleAndBudget(t *testing.T) {
	defs := []skills.Definition{
		{Listing: skills.Listing{Name: "intent", AlwaysOn: true}, Body: "global intent"},
		{Listing: skills.Listing{Name: "builder-role", AlwaysOn: true, Role: "builder"}, Body: "builder only"},
		{Listing: skills.Listing{Name: "critic-role", AlwaysOn: true, Role: "critic"}, Body: "critic only"},
	}

	global := filterAlwaysOnSkills(defs, "", 12000)
	if len(global) != 1 || global[0].Name != "intent" {
		t.Fatalf("empty avatarRole should keep only global skills, got %+v", namesOf(global))
	}

	builder := filterAlwaysOnSkills(defs, "builder", 12000)
	if len(builder) != 2 {
		t.Fatalf("builder should get global+builder, got %+v", namesOf(builder))
	}
	for _, d := range builder {
		if d.Role == "critic" {
			t.Fatal("critic skill must not leak into builder filter")
		}
	}

	tiny := filterAlwaysOnSkills(defs, "builder", 20)
	if len(tiny) < 1 {
		t.Fatal("budget filter should still keep at least the first skill")
	}
	if len(tiny) > 2 {
		t.Fatalf("budget should truncate, got %d", len(tiny))
	}

	generated := filterAlwaysOnSkills([]skills.Definition{
		{Listing: skills.Listing{Name: "intent", AlwaysOn: true}, Body: "global"},
		{Listing: skills.Listing{Name: "20260825-153012-builder-skill", AlwaysOn: true, Role: "builder", Path: "skills/generated/20260825-builder-skill.md"}, Body: "unique per run"},
	}, "builder", 12000)
	if len(generated) != 1 || generated[0].Name != "intent" {
		t.Fatalf("generated per-run skills must not enter always-on prefix, got %+v", namesOf(generated))
	}
}

func namesOf(defs []skills.Definition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}
