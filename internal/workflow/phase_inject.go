package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	phaseDetailPromptRunes = 2200
	priorPhasePromptRunes  = 1200
)

var (
	rePhaseHeadingMD = regexp.MustCompile(`(?im)^#{1,3}\s*phase\s*(\d+)\b`)
	rePhaseHeadingCN = regexp.MustCompile(`(?im)^#{1,3}\s*阶段\s*(\d+)\b`)
)

// StripOtherPhaseSections keeps only the section for keepPhase from a multi-phase
// natural-language request. Language-agnostic: works on "## Phase N" / "## 阶段 N"
// style headings. Content before the first phase heading is kept as shared context.
func StripOtherPhaseSections(input string, keepPhase int) string {
	if keepPhase < 1 || strings.TrimSpace(input) == "" {
		return input
	}
	lines := strings.Split(input, "\n")
	var out []string
	current := 0 // 0 = preamble
	for _, line := range lines {
		if n := phaseHeadingNumber(line); n > 0 {
			current = n
			if current == keepPhase {
				out = append(out, line)
			}
			continue
		}
		if current == 0 || current == keepPhase {
			out = append(out, line)
		}
	}
	trimmed := strings.TrimSpace(strings.Join(out, "\n"))
	if trimmed == "" {
		return input
	}
	return trimmed
}

func phaseHeadingNumber(line string) int {
	trimmed := strings.TrimSpace(line)
	if m := rePhaseHeadingMD.FindStringSubmatch(trimmed); len(m) >= 2 {
		return atoiPhase(m[1])
	}
	if m := rePhaseHeadingCN.FindStringSubmatch(trimmed); len(m) >= 2 {
		return atoiPhase(m[1])
	}
	return 0
}

// ReadPhaseDocBody returns phaseN.md content (capped) for prompt injection.
func ReadPhaseDocBody(projectRoot string, phaseNum int, maxRunes int) string {
	data, err := os.ReadFile(PhaseDocPath(projectRoot, phaseNum))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if IsPhaseDocDeferredStub(text) {
		return ""
	}
	if maxRunes > 0 {
		runes := []rune(text)
		if len(runes) > maxRunes {
			text = string(runes[:maxRunes]) + "\n…(truncated)"
		}
	}
	return text
}

// CompactPhaseDocForPrompt keeps headings and checklist/short bullets so the
// Builder user turn stays small. Long overview/strategy prose is dropped.
func CompactPhaseDocForPrompt(body string, maxRunes int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var keep []string
	blankPending := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			blankPending = true
			continue
		}
		if strings.HasPrefix(t, "<!--") {
			continue
		}
		if !keepPhasePromptLine(t) {
			continue
		}
		if blankPending && len(keep) > 0 {
			keep = append(keep, "")
		}
		blankPending = false
		keep = append(keep, line)
	}
	out := strings.TrimSpace(strings.Join(keep, "\n"))
	if out == "" {
		out = body
	}
	if maxRunes > 0 {
		runes := []rune(out)
		if len(runes) > maxRunes {
			out = string(runes[:maxRunes]) + "\n…(truncated)"
		}
	}
	return out
}

func keepPhasePromptLine(t string) bool {
	if strings.HasPrefix(t, "#") {
		return true
	}
	rest := t
	switch {
	case strings.HasPrefix(rest, "- "), strings.HasPrefix(rest, "* "), strings.HasPrefix(rest, "+ "):
		rest = strings.TrimSpace(rest[1:])
	}
	if strings.HasPrefix(rest, "[") {
		return true
	}
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "+ ") {
		return len(t) <= 180 && !strings.Contains(t, "|")
	}
	return false
}

// ProjectSourceInventory lists key source/manifest paths for LLM phase planning.
// Language-agnostic: any common source extension + manifests.
func ProjectSourceInventory(projectRoot string, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 40
	}
	var files []string
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			switch name {
			case ".git", ".avatars", "avatars", "node_modules", "vendor", "target", "dist", "build", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		rel = filepath.ToSlash(rel)
		base := strings.ToLower(filepath.Base(rel))
		ext := strings.ToLower(filepath.Ext(rel))
		keep := false
		switch base {
		case "go.mod", "go.sum", "cargo.toml", "package.json", "pyproject.toml", "requirements.txt", "tsconfig.json":
			keep = true
		}
		switch ext {
		case ".go", ".py", ".rs", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".sql", ".toml", ".yaml", ".yml":
			keep = true
		}
		if keep {
			files = append(files, rel)
		}
		return nil
	})
	if len(files) == 0 {
		return "(no source files on disk yet)"
	}
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return strings.Join(files, "\n")
}

// InjectPhasePrompt enriches Builder input with THIS phase's checklist and a
// stripped request (no future-phase dump). R4-10 fix.
func InjectPhasePrompt(originalInput string, root string, phaseNum int) string {
	planContent, err := ReadWorkflowDoc(root, "plan")
	if err != nil {
		return originalInput
	}

	phases := extractPhaseInfos(planContent)
	if phaseNum < 1 || phaseNum > len(phases) {
		return originalInput
	}
	ph := phases[phaseNum-1]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== WORKFLOW PHASE %d: %s ===\n", phaseNum, ph.Name))
	sb.WriteString(fmt.Sprintf("Phase goal: %s\n", ph.Goal))
	if ph.File != "" {
		sb.WriteString(fmt.Sprintf("Phase detail file: %s\n", ph.File))
	}
	sb.WriteString("Implement ONLY this phase's Tasks/Verification below. Do NOT implement later phases.\n\n")

	if body := CompactPhaseDocForPrompt(ReadPhaseDocBody(root, phaseNum, 0), phaseDetailPromptRunes); body != "" {
		sb.WriteString("=== PHASE DETAIL (authoritative checklist) ===\n")
		sb.WriteString(body)
		sb.WriteString("\n\n")
	} else if ph.File != "" {
		sb.WriteString(fmt.Sprintf("Phase detail is still a stub — expand %s before coding, or implement only the Phase goal above.\n\n", ph.File))
	}

	if phaseNum > 1 {
		if prev := CompactPhaseDocForPrompt(ReadPhaseDocBody(root, phaseNum-1, 0), priorPhasePromptRunes); prev != "" {
			sb.WriteString(fmt.Sprintf("=== PRIOR PHASE %d (context — already done; extend, do not rewrite) ===\n", phaseNum-1))
			sb.WriteString(prev)
			sb.WriteString("\n\n")
		}
		inv := ProjectSourceInventory(root, 40)
		sb.WriteString("=== FILES ALREADY ON DISK ===\n")
		sb.WriteString(inv)
		sb.WriteString("\n\n")
	}

	scoped := StripOtherPhaseSections(originalInput, phaseNum)
	sb.WriteString("=== USER REQUEST (this phase only) ===\n")
	sb.WriteString(scoped)
	return sb.String()
}
