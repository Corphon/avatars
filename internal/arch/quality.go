package arch

import (
	"strings"
	"unicode/utf8"
)

const (
	// overviewPromptRuneSoft — longer than this and multi-cue → likely a prompt dump.
	overviewPromptRuneSoft = 400
	// overviewPromptRuneHard — absolute max for a real architecture overview.
	overviewPromptRuneHard = 800
)

// QualityIssue is a deterministic architecture.md problem (no LLM).
type QualityIssue struct {
	Code    string // e.g. "overview_prompt_dump", "empty_regpoints"
	Message string
	Hard    bool // Hard=true blocks status:confirmed
}

// QualityIssues returns language-agnostic checks against an ArchDoc.
func QualityIssues(doc *ArchDoc) []QualityIssue {
	if doc == nil {
		return []QualityIssue{{Code: "nil_doc", Message: "architecture document is nil", Hard: true}}
	}
	var out []QualityIssue
	ov := strings.TrimSpace(doc.Overview)
	if ov == "" {
		out = append(out, QualityIssue{
			Code: "empty_overview", Message: "Overview is empty", Hard: true,
		})
	} else if OverviewLooksLikePrompt(ov) {
		out = append(out, QualityIssue{
			Code:    "overview_prompt_dump",
			Message: "Overview looks like a pasted user prompt, not an architecture summary",
			Hard:    true,
		})
	}
	if !hasUsefulRegPoints(doc) {
		out = append(out, QualityIssue{
			Code:    "empty_regpoints",
			Message: "Registration Points are empty — cannot guide multi-file edits",
			Hard:    false, // soft: auto paths stay draft; WARN when combined with hard issues
		})
	}
	if len(doc.EntryPoints) == 0 {
		out = append(out, QualityIssue{
			Code:    "empty_entrypoints",
			Message: "No Entry Points identified",
			Hard:    false,
		})
	}
	return out
}

// OverviewLooksLikePrompt detects prompt-paste Overview text (deterministic).
func OverviewLooksLikePrompt(overview string) bool {
	ov := strings.TrimSpace(overview)
	if ov == "" {
		return false
	}
	n := utf8.RuneCountInString(ov)
	if n >= overviewPromptRuneHard {
		return true
	}
	// Single ultra-long line is almost never a written overview.
	if !strings.Contains(ov, "\n") && n >= overviewPromptRuneSoft {
		return true
	}
	if n < overviewPromptRuneSoft {
		return false
	}
	lower := strings.ToLower(ov)
	cues := 0
	for _, cue := range []string{
		"phase 1", "phase 2", "you must", "must implement", "acceptance criteria",
		"checklist", "jwt", "sqlite", "create a", "build a", "implement a",
		"requirements:", "success criteria", "do not", "please ",
	} {
		if strings.Contains(lower, cue) {
			cues++
		}
	}
	return cues >= 3
}

func hasUsefulRegPoints(doc *ArchDoc) bool {
	for _, rp := range doc.RegPoints {
		if strings.TrimSpace(rp.ChangeType) == "" {
			continue
		}
		for _, f := range rp.Files {
			if strings.TrimSpace(f.Path) != "" {
				return true
			}
		}
	}
	return false
}

// HardQualityBlocks returns hard-fail messages that must block confirmed.
func HardQualityBlocks(doc *ArchDoc) []string {
	var msgs []string
	for _, iss := range QualityIssues(doc) {
		if iss.Hard {
			msgs = append(msgs, iss.Message)
		}
	}
	return msgs
}

// ApplyAutoStatus sets Meta.Status for automated writers (Ensure / refresh).
// Never invents confirmed when Overview is a prompt dump, project is unhealthy,
// or Registration Points are still empty.
//
// projectHealthy: build/compile OK and no hollow entrypoint (caller-probed).
// unknownHealthy: when health was not probed (pre-build Ensure) → stay draft.
func ApplyAutoStatus(doc *ArchDoc, projectHealthy bool, healthKnown bool) []QualityIssue {
	if doc == nil {
		return nil
	}
	issues := QualityIssues(doc)
	hard := HardQualityBlocks(doc)
	softEmptyRP := !hasUsefulRegPoints(doc)

	switch {
	case len(hard) > 0:
		doc.Meta.Status = "draft"
	case healthKnown && !projectHealthy:
		doc.Meta.Status = "draft"
		issues = append(issues, QualityIssue{
			Code: "project_unhealthy", Message: "build/entrypoint unhealthy — cannot confirm", Hard: true,
		})
	case softEmptyRP:
		// Auto-generated scans without LLM RegPoints stay draft (honest).
		doc.Meta.Status = "draft"
	case healthKnown && projectHealthy:
		doc.Meta.Status = "confirmed"
	default:
		// Health unknown (pre-plan Ensure) — draft until refresh after healthy build
		// and/or LLM analyze fills Registration Points.
		doc.Meta.Status = "draft"
	}
	return issues
}

// CanConfirm reports whether a user/CLI confirm should be allowed.
// projectHealthy should reflect current compile + hollow checks when available.
func CanConfirm(doc *ArchDoc, projectHealthy bool) (ok bool, reasons []string) {
	reasons = HardQualityBlocks(doc)
	if !projectHealthy {
		reasons = append(reasons, "project build/entrypoint is unhealthy")
	}
	if !hasUsefulRegPoints(doc) {
		reasons = append(reasons, "Registration Points are empty — run arch --analyze first")
	}
	return len(reasons) == 0, reasons
}

// SanitizeTaskOverview prevents dumping the full NL prompt into Overview.
func SanitizeTaskOverview(taskDesc string) string {
	task := strings.TrimSpace(taskDesc)
	if task == "" {
		return "Project architecture (auto-generated from scan). Run `avatars arch --analyze` to refine."
	}
	if OverviewLooksLikePrompt(task) {
		return "Intent captured from user request (prompt omitted — not an architecture summary). " +
			"Run `avatars arch --analyze` after sources exist to fill layers and registration points."
	}
	// Keep short free-form intents as-is.
	if utf8.RuneCountInString(task) > overviewPromptRuneSoft {
		runes := []rune(task)
		return string(runes[:overviewPromptRuneSoft]) + "…"
	}
	return task
}
