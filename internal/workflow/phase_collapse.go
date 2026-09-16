package workflow

import (
	"regexp"
	"strings"
)

// collapseUnsolicitedPolishPhase drops a trailing Example/polish/README phase
// when the user did not ask for those extras. Library + tests is one phase
// across languages; this is a deterministic post-pass so the Planner prefix
// stays byte-stable.
func collapseUnsolicitedPolishPhase(planStr, taskDesc string) string {
	if userAskedForDocsOrExample(taskDesc) || hasExplicitMultiPhase(taskDesc) {
		return planStr
	}
	meta := ParsePlanMeta(planStr)
	if meta.PhaseCount != 2 || len(meta.Phases) < 2 {
		return planStr
	}
	last := meta.Phases[len(meta.Phases)-1]
	if !looksLikeFillerPolishPhase(last.Name + " " + last.Goal) {
		return planStr
	}
	out := replacePlanPhaseCount(planStr, 1)
	if meta.ActivePhase > 1 {
		out = strings.Replace(out, "**Active Phase**: 2", "**Active Phase**: 1", 1)
	}
	return stripPlanPhaseSection(out, last.Number)
}

func userAskedForDocsOrExample(taskDesc string) bool {
	lower := strings.ToLower(taskDesc)
	needles := []string{
		"example app", "runnable example", "usage example",
		"readme", "polish", "example +",
		"示例", "用法示例", "抛光",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	// Bare "docs" is too noisy (workflow paths). Require README-like asks.
	if strings.Contains(lower, "write a readme") || strings.Contains(lower, "add a readme") ||
		strings.Contains(lower, "写个 readme") || strings.Contains(lower, "写一份 readme") {
		return true
	}
	return false
}

func looksLikeFillerPolishPhase(nameAndGoal string) bool {
	lower := strings.ToLower(strings.TrimSpace(nameAndGoal))
	if lower == "" {
		return false
	}
	needles := []string{
		"example + polish", "example and polish", "examples + polish",
		"runnable example", "finalize docs", "docs/readme",
		"example + docs", "polish",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	hasExample := strings.Contains(lower, "example")
	hasDocs := strings.Contains(lower, "readme") || strings.Contains(lower, "docs")
	return hasExample && hasDocs
}

var planPhaseHeaderRe = regexp.MustCompile(`(?im)^###\s*Phase\s+(\d+)\s*:`)

func stripPlanPhaseSection(planStr string, phaseNum int) string {
	if phaseNum < 2 {
		return planStr
	}
	locs := planPhaseHeaderRe.FindAllStringIndex(planStr, -1)
	if len(locs) == 0 {
		return planStr
	}
	start := -1
	end := len(planStr)
	for i, loc := range locs {
		m := planPhaseHeaderRe.FindStringSubmatch(planStr[loc[0]:loc[1]])
		if len(m) < 2 {
			continue
		}
		var n int
		for _, r := range m[1] {
			if r >= '0' && r <= '9' {
				n = n*10 + int(r-'0')
			}
		}
		if n != phaseNum {
			continue
		}
		start = loc[0]
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			for _, marker := range []string{"\n## Key Decisions", "\n## Risks", "\n## Success Criteria"} {
				if idx := strings.Index(planStr[start:], marker); idx >= 0 {
					end = start + idx
					break
				}
			}
		}
		break
	}
	if start < 0 {
		return planStr
	}
	return strings.TrimSpace(planStr[:start]) + "\n" + planStr[end:]
}
