package skills

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	// path used for embed.FS (always uses forward slashes)
	"path"
)

//go:embed builtin/*.md
var builtinSkillTemplates embed.FS

// builtinHealedMarker is the filename written to the skills root directory
// after a successful self-heal. Its presence prevents re-healing on
// subsequent startups, making the operation idempotent.
const builtinHealedMarker = ".builtin-healed"

// BuiltInSkillNames returns the names of built-in always-on skill templates
// that should be present in every avatars installation.
// S3.11: includes per-role guidance books (Planner/Researcher/Builder/Critic/Synthesizer).
func BuiltInSkillNames() []string {
	return []string{
		"Intent_Routing_Skill.md",
		"Plan_Mode_Skill.md",
		"Workflow_Engine_Skill.md",
		"Planner_Role_Skill.md",
		"Researcher_Role_Skill.md",
		"Builder_Role_Skill.md",
		"Critic_Role_Skill.md",
		"Synthesizer_Role_Skill.md",
	}
}

// SelfHealBuiltinSkills writes any missing built-in skill templates into the
// approved directory, regenerates the Navigator index, and updates the heal
// marker. S3.11: marker must NOT skip missing role builtins on upgrades —
// each name is checked individually (P0-7c).
// When an existing builtin has a lower frontmatter version than the embedded
// template, it is refreshed so role/workflow guidance stays current after upgrades.
//
// Returns the number of skills written and any error encountered.
func (s *Store) SelfHealBuiltinSkills() (int, error) {
	if s == nil {
		return 0, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return 0, err
	}

	names := BuiltInSkillNames()
	written := 0
	for _, name := range names {
		destPath := filepath.Join(s.approvedDir(), name)
		content, err := builtinSkillTemplates.ReadFile(path.Join("builtin", name))
		if err != nil {
			return written, fmt.Errorf("read built-in skill template %s: %w", name, err)
		}
		if existing, statErr := os.ReadFile(destPath); statErr == nil {
			if compareSkillVersion(frontmatterVersion(content), frontmatterVersion(existing)) <= 0 {
				continue // present and up-to-date (or newer local version)
			}
		}
		if err := os.WriteFile(destPath, content, 0o644); err != nil {
			return written, fmt.Errorf("write built-in skill %s: %w", name, err)
		}
		written++
	}

	if written == 0 {
		// Still ensure marker exists for operators inspecting the skills root.
		markerPath := filepath.Join(s.root, builtinHealedMarker)
		if _, err := os.Stat(markerPath); err == nil {
			return 0, nil
		}
		_ = os.WriteFile(markerPath, []byte("self-healed 0 builtin skills (all present)\n"), 0o644)
		return 0, nil
	}

	// Regenerate the navigator to include the new skills.
	if err := s.RegenerateNavigator(); err != nil {
		return written, fmt.Errorf("regenerate navigator after self-heal: %w", err)
	}

	markerPath := filepath.Join(s.root, builtinHealedMarker)
	if err := os.WriteFile(markerPath, []byte(fmt.Sprintf("self-healed %d builtin skills\n", written)), 0o644); err != nil {
		return written, fmt.Errorf("write self-heal marker: %w", err)
	}

	return written, nil
}

// frontmatterVersion extracts the `version:` field from YAML frontmatter.
func frontmatterVersion(content []byte) string {
	text := string(content)
	if !strings.HasPrefix(strings.TrimSpace(text), "---") {
		return ""
	}
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return ""
	}
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "version:"))
		}
	}
	return ""
}

// compareSkillVersion compares dotted numeric versions (e.g. 0.2.0 vs 0.3.0).
// Returns -1 if a<b, 0 if equal, 1 if a>b. Non-numeric segments compare as 0.
func compareSkillVersion(a, b string) int {
	as := strings.Split(strings.TrimSpace(a), ".")
	bs := strings.Split(strings.TrimSpace(b), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
