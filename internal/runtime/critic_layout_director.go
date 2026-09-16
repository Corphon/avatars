package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// layoutCharter is the Critic director's single source of truth for where
// Builders may write (F42). Parallel Builders must follow this charter —
// node titles alone must not invent conflicting trees.
type layoutCharter struct {
	LibraryDir          string // e.g. "geohashn" (no trailing slash)
	ForbidInternalLib   bool   // public library API must not live under internal/<mod>/; private helpers OK
	ForbiddenPathSubstr []string
}

var (
	phaseLibDirRe = regexp.MustCompile(`(?i)(?:top-?level|package(?:\s+path)?(?:\s+建议)?[:：]?\s*|lives at(?: the)?)\s*[\x60'"]?([a-z][\w-]*)/`)
	phaseLibSuggestRe = regexp.MustCompile(`(?i)(?:包路径建议|package\s+path)\s*[:：]?\s*[\x60'"]?([a-z][\w-]+)`)
	phaseNotInternalRe = regexp.MustCompile(`(?i)NOT under\s*[\x60'"]?internal|不要(?:再)?进\s*internal|勿(?:再)?放(?:进|在)\s*internal|禁止.*internal/`)
	bareDocRe          = regexp.MustCompile(`(?i)^doc\.go$`)
)

// resolveLayoutCharter builds the Critic director charter from phase docs + disk.
func resolveLayoutCharter(wd string) layoutCharter {
	c := layoutCharter{
		ForbiddenPathSubstr: []string{
			"internal/debug",
			"internal/doc",
			"/tmp/",
			"tmp/",
			"tmp\\",
			"_check",
			"_cleanup",
			"/dbg/",
		},
	}
	phase := readWorkflowText(wd, "phase1.md")
	req := readWorkflowText(wd, "user_requirement.md")
	blob := phase + "\n" + req

	if phaseNotInternalRe.MatchString(blob) || strings.Contains(blob, "纯库优先") ||
		strings.Contains(strings.ToLower(blob), "pure library") ||
		strings.Contains(blob, "包路径建议") ||
		forbidsCLIScaffold(strings.ToLower(blob)) ||
		wantsPublicLibraryLayout(strings.ToLower(blob)) {
		c.ForbidInternalLib = true
	}
	if m := phaseLibDirRe.FindStringSubmatch(phase); len(m) > 1 {
		c.LibraryDir = m[1]
	}
	if c.LibraryDir == "" {
		if m := phaseLibSuggestRe.FindStringSubmatch(blob); len(m) > 1 {
			c.LibraryDir = m[1]
		}
	}
	if c.LibraryDir == "" {
		if lib := findSiblingLibraryPackage(wd); lib != "" {
			c.LibraryDir = lib
		} else if mod := goModuleDirNameAt(wd); mod != "" {
			c.LibraryDir = mod
		}
	}
	// If top-level library already exists, prefer it and forbid burying under internal/.
	if c.LibraryDir != "" && dirHasImplGoSources(filepath.Join(wd, c.LibraryDir)) {
		c.ForbidInternalLib = true
	}
	return c
}

func readWorkflowText(wd, name string) string {
	paths := []string{
		filepath.Join(wd, "docs", "workflow", name),
		filepath.Join(wd, name),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

// parseGenerateTarget extracts the path from a split Builder title like
// "Generate doc.go AND its test file (final file)".
func parseGenerateTarget(nodeTitle string) string {
	t := strings.TrimSpace(nodeTitle)
	if !strings.HasPrefix(t, "Generate ") {
		return ""
	}
	t = strings.TrimPrefix(t, "Generate ")
	t = strings.TrimSuffix(t, " (final file)")
	if i := strings.Index(t, " AND its test"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(filepath.ToSlash(t))
}

// directorRewriteGenerateTitle applies the Critic charter to a Builder node title.
// Returns rewritten title, whether it changed, and a non-empty reject reason when
// the node must be cancelled (junk / forbidden path).
func directorRewriteGenerateTitle(wd, nodeTitle string) (newTitle string, changed bool, reject string) {
	target := parseGenerateTarget(nodeTitle)
	if target == "" {
		return nodeTitle, false, ""
	}
	c := resolveLayoutCharter(wd)
	mapped, rej := directorMapTarget(c, wd, target)
	if rej != "" {
		return nodeTitle, false, rej
	}
	if mapped == target {
		return nodeTitle, false, ""
	}
	suffix := ""
	if strings.Contains(nodeTitle, " AND its test") {
		suffix = " AND its test file"
	}
	if strings.HasSuffix(nodeTitle, " (final file)") {
		suffix += " (final file)"
	}
	return "Generate " + mapped + suffix, true, ""
}

func directorMapTarget(c layoutCharter, wd, target string) (mapped string, reject string) {
	t := filepath.ToSlash(strings.TrimSpace(target))
	lower := strings.ToLower(t)

	for _, bad := range c.ForbiddenPathSubstr {
		if strings.Contains(lower, strings.ToLower(bad)) {
			return "", "Critic director blocked junk/forbidden path " + t +
				" — not in phase layout charter"
		}
	}
	// Bare package docs → library dir (F34/F42).
	base := filepath.Base(t)
	if (t == base || strings.Count(t, "/") == 0) && bareDocRe.MatchString(base) {
		if c.LibraryDir != "" {
			return c.LibraryDir + "/" + base, ""
		}
	}
	// Public lib under internal/<lib>/ when charter forbids (F38/F42).
	if c.ForbidInternalLib && c.LibraryDir != "" {
		prefix := "internal/" + strings.ToLower(c.LibraryDir)
		libDir := filepath.Join(wd, c.LibraryDir)
		keepSubdir := dirHasImplSources(libDir)
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			rest := strings.TrimPrefix(t, "internal/"+c.LibraryDir)
			rest = strings.TrimPrefix(rest, "internal/"+strings.ToLower(c.LibraryDir))
			rest = strings.TrimPrefix(rest, "/")
			if rest == "" {
				rest = c.LibraryDir + ".go"
			}
			if !keepSubdir {
				if !strings.Contains(rest, "/") {
					return rest, ""
				}
				return filepath.Base(rest), ""
			}
			if !strings.Contains(rest, "/") {
				return c.LibraryDir + "/" + rest, ""
			}
			return c.LibraryDir + "/" + filepath.Base(rest), ""
		}
		// internal/doc → library doc.go
		if strings.HasPrefix(lower, "internal/doc") {
			if keepSubdir {
				return c.LibraryDir + "/doc.go", ""
			}
			return "doc.go", ""
		}
	}
	// Prefer existing top-level lib for bare sources when that dir already
	// holds implementation. Do NOT invent <lib>/ from ForbidInternalLib —
	// that created the F78 root ↔ <lib>/ ping-pong.
	if c.LibraryDir != "" && (t == base) && isSourceFileBasename(base) &&
		!rootEntrypointBasenames[base] {
		if dirHasImplSources(filepath.Join(wd, c.LibraryDir)) {
			return c.LibraryDir + "/" + base, ""
		}
	}
	_ = wd
	return t, ""
}

// criticDirectorLayoutBrief returns a short charter blurb injected into Builder context.
func criticDirectorLayoutBrief(wd string) string {
	c := resolveLayoutCharter(wd)
	var b strings.Builder
	b.WriteString("CRITIC LAYOUT CHARTER (obey; do not invent parallel trees):\n")
	if c.LibraryDir != "" {
		libDir := filepath.Join(wd, c.LibraryDir)
		if dirHasImplSources(libDir) {
			b.WriteString(fmt.Sprintf("- Public library package dir: %s/\n", c.LibraryDir))
			b.WriteString(fmt.Sprintf("- Package docs: file-header comments in %s/*.go only. Do NOT create standalone doc.go unless the user explicitly asks. NEVER write bare root doc.go or internal/doc/\n", c.LibraryDir))
		} else {
			b.WriteString(fmt.Sprintf("- Public library lives at repo root (`%s.go` / tests). Do NOT create %s/ or internal/%s/\n", c.LibraryDir, c.LibraryDir, c.LibraryDir))
			b.WriteString("- Package docs: file-header comments in the root .go files. NEVER write bare extra doc.go or internal/doc/\n")
		}
	}
	if c.ForbidInternalLib {
		b.WriteString("- Public library API stays at repo root or <mod>/. Do NOT bury it under internal/<mod>/.\n")
		b.WriteString("- Genuine private helpers may live under internal/<helper>/ with their OWN package name. Do NOT put package <mod> files under internal/<other>/.\n")
	}
	b.WriteString("- Forbidden: internal/debug, tmp/, _check, hollow internal/doc\n")
	if forbidsCLIScaffold(strings.ToLower(readLayoutTaskBlob(wd))) {
		b.WriteString("- NO CLI / NO root main / NO cmd/ scaffold — library sources + tests only\n")
	} else {
		b.WriteString("- CLI (if any): cmd/<name>/ only\n")
	}
	b.WriteString("- architecture.md is advisory; phase1.md + this charter win on conflicts\n")
	return b.String()
}

// readLayoutTaskBlob gathers phase/task text used for negative-constraint charter lines.
func readLayoutTaskBlob(wd string) string {
	var parts []string
	for _, name := range []string{
		filepath.Join(wd, "docs", "workflow", "phase1.md"),
		filepath.Join(wd, "docs", "workflow", "avatars_plan.md"),
	} {
		if data, err := os.ReadFile(name); err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}
