package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/workflow"
)

var claimedPathRe = regexp.MustCompile("`([A-Za-z0-9_.@+/-]+\\.(?:go|py|rs|js|jsx|ts|tsx|mjs|cjs|toml|mod|json|md|sql|yml|yaml))`")

// writeFailureDeliveryAnswer writes a deterministic user-facing Delivery Summary
// when Critic hard-fails (F65). Ensures NL runs still answer what shipped
// and what remains even if Synthesizer later also runs or is degraded.
func writeFailureDeliveryAnswer(wd, errMsg string, changed []string) error {
	wd = strings.TrimSpace(wd)
	if wd == "" {
		wd = "."
	}
	errMsg = strings.TrimSpace(errMsg)
	if errMsg == "" {
		errMsg = "unknown critic/build failure"
	}

	var b strings.Builder
	b.WriteString("# Delivery Summary\n\n")
	b.WriteString("This run **did not produce a usable delivery**: the build/quality gate failed. Files written and the blocking error are below.\n\n")
	b.WriteString("## Status\n\n")
	b.WriteString("- **needs_remediation** — Critic/build gate failed\n\n")
	b.WriteString("## What Changed\n\n")
	writeExistingChangedBullets(&b, wd, changed)
	b.WriteString("\n## Why It Failed\n\n")
	b.WriteString("```\n")
	b.WriteString(errMsg)
	b.WriteString("\n```\n\n")
	b.WriteString("## How to Verify / Next Steps\n\n")
	b.WriteString("1. Fix the compile/test/layout error above.\n")
	b.WriteString("2. Re-run the same task (or `go test ./...` / project health check locally).\n")
	b.WriteString("3. Do not treat checklist progress as green delivery while build is red.\n")
	b.WriteString("\n---\n")
	b.WriteString(fmt.Sprintf("_Generated %s by avatars failure-delivery path (F65)._\n", time.Now().Format(time.RFC3339)))

	out := filepath.Join(wd, "answer.md")
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

// reconcileFinalDeliveryAnswer overwrites answer.md so the user-facing summary
// matches final run gates (F67) and on-disk paths (F70).
func reconcileFinalDeliveryAnswer(wd, status, synthText string, buildOK, buildOKKnown bool, healthErr string, changed []string) error {
	wd = strings.TrimSpace(wd)
	if wd == "" {
		wd = "."
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "completed_unverified"
	}

	finalRed := (buildOKKnown && !buildOK) || strings.TrimSpace(healthErr) != "" ||
		status == "needs_remediation" || status == "failed"
	synthLower := strings.ToLower(synthText)
	synthClaimsFail := strings.Contains(synthLower, "build failed") ||
		strings.Contains(synthLower, "needs_remediation") ||
		strings.Contains(synthLower, "post_build_failed") ||
		strings.Contains(synthLower, "语法错误") ||
		strings.Contains(synthLower, "syntax error") ||
		strings.Contains(synthLower, "构建失败") ||
		(strings.Contains(synthLower, "jwt_test.go:") && strings.Contains(synthLower, "expected"))

	contradicts := !finalRed && synthClaimsFail
	missingPaths := claimedPathsMissingOnDisk(wd, synthText)
	diskSources := listAuthoritativeSourcePaths(wd)

	var b strings.Builder
	b.WriteString("# Delivery Summary\n\n")
	if finalRed {
		b.WriteString("This run **did not fully pass the gate**: final health is still red, or status is needs_remediation/failed.\n\n")
	} else {
		b.WriteString("Final health for this run is **green** (compile/test gate). If post_build_failed appeared earlier, this section is authoritative.\n\n")
	}
	b.WriteString("## Status\n\n")
	b.WriteString("- **")
	b.WriteString(status)
	b.WriteString("**")
	if buildOKKnown {
		if buildOK {
			b.WriteString(" — build_ok=true")
		} else {
			b.WriteString(" — build_ok=false")
		}
	}
	b.WriteString("\n")
	if he := strings.TrimSpace(healthErr); he != "" {
		b.WriteString("- health: `")
		b.WriteString(he)
		b.WriteString("`\n")
	}
	writePlanProgressSection(&b, wd)
	if strings.Contains(strings.ToLower(synthText), "max tool turns") ||
		strings.Contains(strings.ToLower(synthText), "BUILDER NOTE: code generation hit max tool turns") {
		if finalRed {
			b.WriteString("- builder: max tool turns reached; compile/test still RED — not a green delivery (F76)\n")
		} else {
			b.WriteString("- builder: max tool turns reached; delivery uses on-disk green files (F76)\n")
		}
	}
	noToolchainLie, fixedLie := synthesizerHonestyFlags(synthText, healthErr, finalRed)

	b.WriteString("\n## What Changed\n\n")
	changed = reconcileChangedPathsToDisk(wd, changed)
	writeExistingChangedBullets(&b, wd, mergeUniquePaths(changed, diskSources))
	if len(missingPaths) > 0 {
		b.WriteString("\n## Path Corrections\n\n")
		b.WriteString("The model summary mentioned these paths that **do not exist on disk** (ignore them):\n\n")
		for _, p := range missingPaths {
			b.WriteString("- `")
			b.WriteString(p)
			b.WriteString("` — NOT ON DISK")
			if alt := suggestUnburiedAlt(wd, p); alt != "" {
				b.WriteString(" (did you mean `")
				b.WriteString(alt)
				b.WriteString("`?)")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## How to Verify\n\n")
	b.WriteString("```bash\n# from the project workspace\ngo test ./...   # or pytest / npm test / cargo test\n```\n\n")
	if contradicts || len(missingPaths) > 0 || noToolchainLie || fixedLie {
		b.WriteString("## Correction Note\n\n")
		if contradicts {
			b.WriteString("- The model summary still cites a stale compile failure; **Status was rewritten from the final gate**.\n")
		}
		if len(missingPaths) > 0 {
			b.WriteString("- The model summary invented file paths that are not on disk; **What Changed lists only files that exist**.\n")
		}
		if noToolchainLie {
			b.WriteString("- The model claimed the language toolchain is missing, but final health actually ran compile/test; **trust health, do not hide a red gate behind 'no toolchain'** (F90).\n")
		}
		if fixedLie {
			b.WriteString("- The model claimed the issue is already fixed (or that an edit tool was applied), but final health is still red; **do not call it fixed until the gate is green** (F90).\n")
		}
		b.WriteString("\n")
	}
	apiMismatches := detectMismatchedUsageExamples(wd, synthText)
	if len(apiMismatches) > 0 {
		b.WriteString("## Correct API\n\n")
		b.WriteString("Model usage examples do not match on-disk exported signatures (F93). Use these:\n\n")
		for _, line := range apiMismatches {
			b.WriteString("- ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if trimmed := strings.TrimSpace(synthText); trimmed != "" && !finalRed {
		if shouldOmitUnevidencedFallbackNotes(trimmed, status) {
			b.WriteString("## Model Notes\n\n")
			b.WriteString("Synthesis unavailable (provider billing/quota or empty synthesis). Fallback project-shape guesses were omitted because they are not evidence-backed.\n")
		} else {
			b.WriteString("## Model Notes")
			if contradicts || len(missingPaths) > 0 || noToolchainLie || fixedLie || len(apiMismatches) > 0 {
				b.WriteString(" (may be stale / path-hallucinated)")
			}
			b.WriteString("\n\n")
			trimmed = annotateMissingPathsInText(trimmed, missingPaths)
			trimmed = strings.ToValidUTF8(trimmed, "")
			trimmed = stripContradictoryPhaseClaims(trimmed, wd)
			if max := 1800; len([]rune(trimmed)) > max {
				trimmed = truncateRunes(trimmed, max) + "\n"
			}
			b.WriteString(trimmed)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n---\n")
	b.WriteString(fmt.Sprintf("_Reconciled %s by avatars final-delivery path._\n", time.Now().Format(time.RFC3339)))

	if err := os.WriteFile(filepath.Join(wd, "answer.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	rewriteHumanDeliveryIfAnalysisTemplate(wd, b.String())
	return nil
}

func writePlanProgressSection(b *strings.Builder, wd string) {
	planPath := filepath.Join(wd, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return
	}
	meta := workflow.ParsePlanMeta(string(data))
	if meta.PhaseCount <= 0 && meta.ActivePhase <= 0 {
		return
	}
	b.WriteString("\n## Progress\n\n")
	if meta.PhaseCount > 0 {
		active := meta.ActivePhase
		if active <= 0 {
			active = 1
		}
		b.WriteString(fmt.Sprintf("- Active phase: %d of %d\n", active, meta.PhaseCount))
	} else if meta.ActivePhase > 0 {
		b.WriteString(fmt.Sprintf("- Active phase: %d\n", meta.ActivePhase))
	}
	todoPath := filepath.Join(wd, workflow.DocPaths["todo"])
	if todoBytes, err := os.ReadFile(todoPath); err == nil {
		done, total := workflow.CountTodoProgressFromContent(string(todoBytes))
		if total > 0 {
			b.WriteString(fmt.Sprintf("- Checklist: %d/%d done\n", done, total))
		}
	}
}

func stripContradictoryPhaseClaims(text, wd string) string {
	planPath := filepath.Join(wd, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return text
	}
	meta := workflow.ParsePlanMeta(string(data))
	if meta.PhaseCount <= 1 {
		return text
	}
	repls := []string{
		"the only phase", "the unique phase", "only phase",
		"唯一阶段", "就一个阶段", "只有一个阶段", "仅有一个阶段",
	}
	out := text
	for _, p := range repls {
		out = strings.ReplaceAll(out, p, "current phase")
	}
	return out
}

// rewriteHumanDeliveryIfAnalysisTemplate replaces DELIVERY.md (and 交付.md)
// when it is still the Analysis Report template (F110). Uses the reconciled
// delivery summary — never invents APIs. Does not touch the LLM prompt prefix.
func rewriteHumanDeliveryIfAnalysisTemplate(wd, deliverySummary string) {
	wd = nonEmptyWD(wd)
	candidates := []string{"DELIVERY.md", "delivery.md", "交付.md"}
	ents, _ := os.ReadDir(wd)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if isHumanDeliveryDocPath(e.Name()) {
			candidates = append(candidates, e.Name())
		}
	}
	seen := map[string]bool{}
	for _, name := range candidates {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		path := filepath.Join(wd, name)
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if !isGeneratedAnalysisReportPath(path) && !strings.HasPrefix(strings.TrimSpace(string(body)), "# Analysis Report") {
			continue
		}
		out := strings.TrimSpace(deliverySummary)
		if out == "" || !strings.Contains(out, "# Delivery Summary") {
			out = "# Delivery\n\nThis file previously held an analysis-report template. See `answer.md` for the on-disk delivery summary.\n"
		}
		_ = os.WriteFile(path, []byte(out+"\n"), 0o644)
	}
}

func shouldOmitUnevidencedFallbackNotes(synthText, status string) bool {
	if llm.IsQuotaOrBillingFailure(status) || llm.IsQuotaOrBillingFailure(synthText) {
		return true
	}
	lower := strings.ToLower(synthText)
	if strings.Contains(lower, "likely project shape: a wails desktop app") {
		return true
	}
	if strings.Contains(lower, "desktop excel") || strings.Contains(lower, "excel + sql workbench") {
		return true
	}
	return strings.Contains(lower, "deterministic fallback report") &&
		strings.Contains(lower, "wails desktop")
}

func synthesizerHonestyFlags(synthText, healthErr string, finalRed bool) (noToolchainLie, fixedLie bool) {
	sl := strings.ToLower(synthText)
	he := strings.ToLower(strings.TrimSpace(healthErr))
	ranGate := he != "" && !strings.Contains(he, "host toolchain unavailable")
	claimsNoTC := strings.Contains(sl, "无 go 工具链") || strings.Contains(sl, "没有 go 工具链") ||
		strings.Contains(sl, "未安装 go") || strings.Contains(sl, "no go toolchain") ||
		strings.Contains(sl, "go not installed") || strings.Contains(sl, "no toolchain") ||
		strings.Contains(sl, "toolchain not available") ||
		(strings.Contains(sl, "no python") && strings.Contains(sl, "toolchain")) ||
		(strings.Contains(sl, "cargo not found") && !strings.Contains(he, "cargo not found"))
	if ranGate && claimsNoTC {
		noToolchainLie = true
	}
	claimsFixed := strings.Contains(synthText, "已修复") || strings.Contains(sl, "already fixed") ||
		strings.Contains(sl, "all tests passed") || strings.Contains(sl, "tests pass now") ||
		(strings.Contains(synthText, "已把") && strings.Contains(sl, "修复")) ||
		strings.Contains(sl, "precise_edit") || strings.Contains(sl, "edit_file") ||
		strings.Contains(sl, "fix applied") || strings.Contains(synthText, "已改成") ||
		strings.Contains(synthText, "已修复类型")
	if finalRed && claimsFixed {
		fixedLie = true
	}
	return
}

func writeExistingChangedBullets(b *strings.Builder, wd string, paths []string) {
	seen := map[string]bool{}
	n := 0
	for _, f := range paths {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f == "" || seen[f] {
			continue
		}
		// Prefer project-relative; skip abs outside.
		rel := f
		if filepath.IsAbs(f) {
			if r, err := filepath.Rel(wd, f); err == nil && !strings.HasPrefix(r, "..") {
				rel = filepath.ToSlash(r)
			} else {
				continue
			}
		}
		full := rel
		if !filepath.IsAbs(full) {
			full = filepath.Join(wd, filepath.FromSlash(rel))
		}
		if _, err := os.Stat(full); err != nil {
			continue
		}
		seen[rel] = true
		n++
		b.WriteString("- `")
		b.WriteString(rel)
		b.WriteString("`\n")
	}
	if n == 0 {
		b.WriteString("- (no verified on-disk source files recorded)\n")
	}
}

func claimedPathsMissingOnDisk(wd, text string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, m := range claimedPathRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		p := filepath.ToSlash(strings.TrimSpace(m[1]))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if claimedPathExistsOnDisk(wd, p) {
			continue
		}
		missing = append(missing, p)
	}
	return missing
}

func claimedPathExistsOnDisk(wd, claimed string) bool {
	for _, cand := range claimedPathCandidates(claimed) {
		full := cand
		if !filepath.IsAbs(full) {
			full = filepath.Join(wd, filepath.FromSlash(cand))
		}
		if _, err := os.Stat(full); err == nil {
			return true
		}
	}
	return false
}

func claimedPathCandidates(claimed string) []string {
	p := filepath.ToSlash(strings.TrimSpace(claimed))
	if p == "" {
		return nil
	}
	out := []string{p}
	base := filepath.Base(p)
	if !isWorkflowArtifactBasename(base) {
		return out
	}
	if !strings.Contains(strings.ToLower(p), "docs/workflow/") {
		out = append(out, filepath.ToSlash(filepath.Join("docs", "workflow", base)))
	}
	if !strings.HasPrefix(strings.ToLower(p), "docs/") {
		out = append(out, filepath.ToSlash(filepath.Join("docs", base)))
	}
	return out
}

func isWorkflowArtifactBasename(base string) bool {
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "avatars_todo.md", "avatars_plan.md", "process_record.md",
		"process_record.yaml", "architecture.md":
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(base))
	return strings.HasPrefix(lower, "phase") && strings.HasSuffix(lower, ".md")
}

func annotateMissingPathsInText(text string, missing []string) string {
	for _, p := range missing {
		text = strings.ReplaceAll(text, "`"+p+"`", "`"+p+"` ⚠NOT_ON_DISK")
	}
	return text
}

func suggestUnburiedAlt(wd, claimed string) string {
	claimed = filepath.ToSlash(claimed)
	if base := filepath.Base(claimed); isWorkflowArtifactBasename(base) {
		alt := filepath.ToSlash(filepath.Join("docs", "workflow", base))
		if claimedPathExistsOnDisk(wd, alt) && !strings.EqualFold(claimed, alt) {
			return alt
		}
	}
	lower := strings.ToLower(claimed)
	prefixes := []string{"internal/", "pkg/", "lib/", "private/"}
	for _, pre := range prefixes {
		if !strings.HasPrefix(lower, pre) {
			continue
		}
		alt := claimed[len(pre):]
		if alt == "" {
			continue
		}
		full := filepath.Join(wd, filepath.FromSlash(alt))
		if _, err := os.Stat(full); err == nil {
			return alt
		}
		// Also try basename under a top-level package dir that exists.
		base := filepath.Base(alt)
		dir := filepath.Dir(alt)
		if dir != "." && dir != "" {
			cand := filepath.Join(wd, filepath.FromSlash(dir), base)
			if _, err := os.Stat(cand); err == nil {
				return filepath.ToSlash(filepath.Join(dir, base))
			}
		}
	}
	return ""
}

// purgeEmptyBurialDirs removes leftover empty burial roots after remaps (F71).
// Cross-lang: internal/ (Go), plus empty private/ when nothing remains.
func purgeEmptyBurialDirs(wd string) []string {
	wd = nonEmptyWD(wd)
	var removed []string
	// Cross-lang private-dir shells: Go internal/, Python/JS _internal/ and
	// src/internal/, plus empty private/.
	for _, root := range []string{"internal", "private", "_internal", filepath.Join("src", "internal")} {
		full := filepath.Join(wd, root)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		if purgeEmptyDirTree(full) {
			removed = append(removed, filepath.ToSlash(root)+"/")
		}
	}
	// F78: drop empty public-package shells (ttlcache/) left after folding
	// pkg/pkg.ext back to a root source (cross-lang).
	for name := range projectPackageNamesAt(wd) {
		if name == "" || name == "." || name == "internal" || name == "src" || name == "app" {
			continue
		}
		full := filepath.Join(wd, name)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		if dirHasImplSources(full) {
			continue
		}
		if purgeEmptyDirTree(full) {
			removed = append(removed, name+"/")
		}
	}
	return removed
}

func purgeEmptyDirTree(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	allGone := true
	for _, e := range ents {
		child := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if !purgeEmptyDirTree(child) {
				allGone = false
			}
			continue
		}
		// Non-empty file → keep tree.
		allGone = false
	}
	if !allGone {
		return false
	}
	_ = os.Remove(dir)
	return true
}

func listAuthoritativeSourcePaths(wd string) []string {
	var out []string
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "venv", ".venv", "node_modules", "vendor", "tmp", "temp", "scratch":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		switch {
		case strings.HasSuffix(name, ".go"), strings.HasSuffix(name, ".py"),
			strings.HasSuffix(name, ".rs"), strings.HasSuffix(name, ".js"),
			strings.HasSuffix(name, ".ts"), strings.HasSuffix(name, ".tsx"),
			name == "go.mod", name == "cargo.toml", name == "package.json",
			name == "pyproject.toml", name == "requirements.txt":
			rel, relErr := filepath.Rel(wd, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "docs/") || strings.HasPrefix(rel, "answer.md") {
				return nil
			}
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func implementationSourcesFrom(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || seen[p] || !isImplementationSourcePath(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func isImplementationSourcePath(p string) bool {
	p = filepath.ToSlash(strings.ToLower(p))
	base := filepath.Base(p)
	if strings.HasPrefix(base, "continue") || strings.HasPrefix(base, "round") {
		return false
	}
	if base == "answer.md" || base == "architecture.md" {
		return false
	}
	if strings.Contains(p, "docs/workflow/") {
		return false
	}
	switch filepath.Ext(p) {
	case ".go", ".py", ".rs", ".js", ".ts", ".tsx", ".jsx", ".java", ".cs", ".kt":
		return true
	default:
		return false
	}
}

func mergeUniquePaths(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, xs := range [][]string{a, b} {
		for _, p := range xs {
			p = filepath.ToSlash(strings.TrimSpace(p))
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// softFailCriticDelivery records a Critic hard failure without aborting the
// multi-avatar run, so Synthesizer can still speak to the user (F65).
func (e *Engine) softFailCriticDelivery(wd, msg string) workflowNodeWorkResult {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "critic hard failure"
	}
	var changed []string
	if e != nil {
		e.criticHardFailure = msg
		e.phaseAdvanceBlocked = mergePhaseBlock(e.phaseAdvanceBlocked, "build_failed")
		changed = append([]string(nil), e.changedFiles...)
	}
	_ = writeFailureDeliveryAnswer(wd, msg, changed)
	return workflowNodeWorkResult{
		criticChallenge: "HARD FAILURE: " + msg,
	}
}
