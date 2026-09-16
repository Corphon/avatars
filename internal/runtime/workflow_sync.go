package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/arch"
	"avatars/internal/llm"
	"avatars/internal/projectfiles"
	"avatars/internal/workflow"
)

// WorkflowSyncCtx carries pre-computed workflow state through the post-run
// defer chain. W2: Eliminates independent IO per function — build/test runs
// exactly once and all consumers share the same results.
type WorkflowSyncCtx struct {
	ProjectRoot  string
	BuildOK      bool
	TestOK       bool
	ChangedFiles []string
	MarkedCount  int
	LLM          llm.Client // optional — R3-9C arch enrich after build
	Ctx          context.Context
	OnSyncErr    func(op string, err error)
}

func (ctx WorkflowSyncCtx) noteSyncErr(op string, err error) {
	if err == nil || ctx.OnSyncErr == nil {
		return
	}
	ctx.OnSyncErr(op, err)
}

// syncWorkflowAfterBuild updates workflow state after Builder completes.
// W1: Accepts pre-computed buildOK/testOK/changedFiles instead of running
// its own go build/go test. The caller (defer chain in loop.go) runs
// verification once and passes results here.
//
// This ensures workflow docs are LLM's progress memory: when the LLM
// reads them on a subsequent run, it sees what was actually done.
func syncWorkflowAfterBuild(ctx WorkflowSyncCtx) WorkflowSyncCtx {
	wd := ctx.ProjectRoot
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return ctx
		}
		ctx.ProjectRoot = wd
	}

	// F71: clean empty burial dirs left after remaps (cross-lang: internal/, private/).
	_ = purgeEmptyBurialDirs(wd)

	// 1. Evidence-based marking: use build/test results to mark checklist.
	// P4: require BuildOK — matching loop.go defer — so failed builds cannot 虚勾.
	marked := 0
	if ctx.BuildOK && len(ctx.ChangedFiles) > 0 {
		marked += workflow.TryAutoMarkDone(wd, "Builder code generation", ctx.ChangedFiles)
	}

	// 2. Mark based on verification evidence.
	if ctx.BuildOK {
		marked += evidenceMark(wd, "go build", "go build ./... passes", "go build")
	}
	if ctx.TestOK {
		marked += evidenceMark(wd, "go test", "go test ./... passes", "go test")
		marked += evidenceMark(wd, "test", "所有测试通过", "test")
		marked += evidenceMark(wd, "test", "测试通过", "test")
	}

	// 3. Mark plan success criteria if build+test both pass.
	if ctx.BuildOK && ctx.TestOK {
		marked += markPlanSuccessCriteria(wd)
	}

	// 4. Sync marks back to phase docs.
	if marked > 0 {
		_ = workflow.SyncMarksToPhase(wd)
	}
	ctx.MarkedCount = marked

	// 5. Update architecture (R3-9: refresh imports/layers + quality gate).
	updateArchitectureAfterBuild(wd, ctx.BuildOK, ctx.LLM, ctx.Ctx)

	// 6. Log summary for LLM context.
	if ctx.BuildOK && ctx.TestOK {
		if err := workflow.RecordTaskCompletion(wd,
			"Build and tests passing",
			fmt.Sprintf("go build ✅ go test ✅ — %d files, %d items marked [x]", len(ctx.ChangedFiles), marked)); err != nil {
			ctx.noteSyncErr("RecordTaskCompletion", err)
		}
	}

	return ctx
}

// RecordPhaseCompletion writes a structured phase completion entry to
// SQLite (via RecordTaskCompletion). Called when TryAdvancePhase or
// CriticDecidePhaseAdvance advances to the next phase.
//
// M1: Hazards/pitfalls now go to SQLite warm_lessons (SoT), not process_record.md.
// The .md is regenerated from SQLite at the end of each run.
//
// Records:
//   - Hazards: build/test failures discovered during this phase
//   - Pitfalls: repeated errors, patterns to avoid
//   - What was accomplished and what the next phase needs
func RecordPhaseCompletion(wd string, completedPhase int, buildOK bool, testOK bool, fileCount int) {
	phaseLabel := fmt.Sprintf("Phase %d completed", completedPhase)
	summary := fmt.Sprintf("Phase %d: build=%v test=%v files=%d", completedPhase, buildOK, testOK, fileCount)

	// Record hazards if build/test failed.
	learned := ""
	if !buildOK {
		learned += "HAZARD: go build failed during Phase " + fmt.Sprintf("%d", completedPhase) +
			" — Critic should dispatch Builder to fix before advancing. "
	}
	if !testOK {
		learned += "HAZARD: go test failed during Phase " + fmt.Sprintf("%d", completedPhase) +
			" — tests must pass before phase is considered complete. "
	}

	// M1: Record completion to SQLite (SoT). process_record.md is regenerated
	// from SQLite at the end of each run, no longer written incrementally.
	// Phase status update is handled by UpdateActivePhase in TryAdvancePhase.
	_ = workflow.RecordTaskCompletion(wd, phaseLabel, summary, []string{}, learned)
}

// BuildProgressSummary returns a user-facing progress report: what was done,
// what remains, and what to do next. Called after each run to give the user
// clear feedback on workflow state.
//
// Batch4/I: when phaseAdvanceBlocked is non-empty (or buildOKKnown&&!buildOK),
// never claim "Next run advances to Phase N" — checklist can be green while
// the health gate still blocks advance.
func BuildProgressSummary(wd string) string {
	return BuildProgressSummaryWithGate(wd, true, false, "")
}

// BuildProgressSummaryWithGate is the honest footer used by the CLI after a run.
func BuildProgressSummaryWithGate(wd string, buildOK, buildOKKnown bool, phaseAdvanceBlocked string) string {
	planContent, err := workflow.ReadWorkflowDoc(wd, "plan")
	if err != nil {
		return ""
	}
	meta := workflow.ParsePlanMeta(planContent)
	if meta.ActivePhase < 1 {
		return ""
	}

	displayPhase := meta.ActivePhase
	if todoContent, todoErr := workflow.ReadWorkflowDoc(wd, "todo"); todoErr == nil {
		if n := workflow.ParseTodoActivePhase(todoContent); n > 0 {
			displayPhase = n
		}
	}

	done, total := workflow.CountTodoProgress(wd)
	remaining := total - done
	blocked := strings.TrimSpace(phaseAdvanceBlocked)
	if blocked == "" && buildOKKnown && !buildOK {
		blocked = "build_failed"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n---\nPhase %d/%d", displayPhase, meta.PhaseCount))
	if blocked == "confirm_failed" || blocked == "construct_failed" {
		// F11: planning quality failure — do not claim checklist/build state.
		b.WriteString(fmt.Sprintf(". Plan/phase confirm blocked (%s) — fix Active Phase Tasks/Scope coverage, then re-confirm before Phase %d.", blocked, displayPhase+1))
	} else if blocked != "" {
		b.WriteString(fmt.Sprintf(": %d/%d tasks done", done, total))
		if remaining == 0 {
			b.WriteString(" (checklist complete)")
		} else {
			b.WriteString(fmt.Sprintf(", %d remaining", remaining))
		}
		b.WriteString(fmt.Sprintf(". Phase advance blocked (%s) — fix build/docs before Phase %d.", blocked, displayPhase+1))
	} else if remaining == 0 && displayPhase < meta.PhaseCount {
		b.WriteString(fmt.Sprintf(" — all tasks complete. Next run advances to Phase %d.", displayPhase+1))
	} else if remaining == 0 {
		b.WriteString(" — project complete.")
	} else {
		b.WriteString(fmt.Sprintf(": %d/%d tasks done, %d remaining.", done, total, remaining))
		if meta.PhaseCount > 1 && displayPhase < meta.PhaseCount {
			b.WriteString(fmt.Sprintf("\nNext: finish these %d tasks to advance to Phase %d.", remaining, displayPhase+1))
		}
	}
	b.WriteString("\n---")
	return b.String()
}

// evidenceMark searches todo and phase docs for items containing the given
// keyword, and marks them [x] if the associated verification passed.
// Returns count of items marked.
func evidenceMark(root, keyword, matchPhrase, evidence string) int {
	marked := 0

	// Mark in todo — Phase Checklist section only (S2.4).
	todoPath := filepath.Join(root, "docs", "workflow", "avatars_todo.md")
	if data, err := os.ReadFile(todoPath); err == nil {
		content := string(data)
		lines := strings.Split(content, "\n")
		inChecklist := false
		sectionMarked := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "## Phase ") && strings.Contains(trimmed, "Checklist") {
				inChecklist = true
				continue
			}
			if inChecklist && strings.HasPrefix(trimmed, "## ") {
				inChecklist = false
			}
			if !inChecklist {
				continue
			}
			if strings.HasPrefix(trimmed, "- [ ]") && strings.Contains(strings.ToLower(trimmed), strings.ToLower(keyword)) {
				lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
				sectionMarked++
			}
		}
		if sectionMarked > 0 {
			_ = os.WriteFile(todoPath, []byte(strings.Join(lines, "\n")), 0644)
			marked += sectionMarked
		}
	}

	// Mark in plan success criteria — Active Phase only (L2).
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	if data, err := os.ReadFile(planPath); err == nil {
		content := string(data)
		meta := workflow.ParsePlanMeta(content)
		active := meta.ActivePhase
		if active < 1 {
			active = 1
		}
		updated := markSuccessCriteriaContaining(root, content, keyword, matchPhrase, active)
		if updated != content {
			_ = os.WriteFile(planPath, []byte(updated), 0644)
			marked++
			_ = setActivePhaseStatusForEvidence(root, active)
		}
	}

	return marked
}

// markSuccessCriteriaContaining marks Success Criteria bullets containing
// keyword/matchPhrase, gated to Active Phase when the bullet is phase-scoped.
func markSuccessCriteriaContaining(projectRoot, mdContent, keyword, matchPhrase string, activePhase int) string {
	scStart := strings.Index(mdContent, "## Success Criteria")
	if scStart < 0 {
		return mdContent
	}
	afterSC := mdContent[scStart:]
	scEnd := strings.Index(afterSC[1:], "\n## ")
	var scSection string
	var rest string
	if scEnd > 0 {
		scSection = afterSC[:scEnd+1]
		rest = afterSC[scEnd+1:]
	} else {
		scSection = afterSC
	}
	if matchPhrase != "" {
		scSection = strings.ReplaceAll(scSection, "- [ ] "+matchPhrase, "- [x] "+matchPhrase)
	}
	kw := strings.ToLower(keyword)
	lines := strings.Split(scSection, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [ ]") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if n := workflow.SuccessCriteriaPhaseNum(lower); n > 0 && n != activePhase {
			continue
		}
		// R3: do not mark phase-scoped Criteria while that phase's todo is open.
		if n := workflow.SuccessCriteriaPhaseNum(lower); n > 0 && !workflow.PhaseChecklistReadyForCriteria(projectRoot, n) {
			continue
		}
		if activePhase > 0 && workflow.SuccessCriteriaPhaseNum(lower) == 0 &&
			!workflow.PhaseChecklistReadyForCriteria(projectRoot, activePhase) {
			continue
		}
		if kw != "" && strings.Contains(lower, kw) {
			lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
		}
	}
	return mdContent[:scStart] + strings.Join(lines, "\n") + rest
}

// markPlanSuccessCriteria marks success criteria in avatars_plan.md
// when build+test both pass — Active Phase only (L2).
func markPlanSuccessCriteria(root string) int {
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return 0
	}
	content := string(data)
	meta := workflow.ParsePlanMeta(content)
	active := meta.ActivePhase
	if active < 1 {
		active = 1
	}
	marked := 0

	// Find the Success Criteria section and mark build/test related items.
	inCriteria := false
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "## Success Criteria") {
			inCriteria = true
			continue
		}
		if inCriteria && strings.HasPrefix(trimmed, "## ") {
			break
		}
		if inCriteria && strings.HasPrefix(trimmed, "- [ ]") {
			lower := strings.ToLower(trimmed)
			// L2: never mark future-phase criteria (e.g. "Phase 5: … test …").
			if n := workflow.SuccessCriteriaPhaseNum(lower); n > 0 && n != active {
				continue
			}
			// R3: gate Criteria marks on Active Phase todo readiness (same as TryAutoMarkPlanCriteria).
			phaseForGate := workflow.SuccessCriteriaPhaseNum(lower)
			if phaseForGate < 1 {
				phaseForGate = active
			}
			if phaseForGate > 0 && !workflow.PhaseChecklistReadyForCriteria(root, phaseForGate) {
				continue
			}
			// Prefer phase-scoped Active Phase lines; avoid bare "test"/"pass"
			// matching across all criteria.
			if n := workflow.SuccessCriteriaPhaseNum(lower); n == active {
				if criteriaLooksLikeCoverage(lower) {
					continue // W4
				}
				if criteriaLooksLikeTestsPass(lower) && !testsEvidenceOK(root) {
					continue
				}
				if criteriaLooksLikeHealth(lower) && !healthPayloadLooksOK(root) {
					continue
				}
				if criteriaLooksLikeStructure(lower) && !workflow.PlanStructurePathsOK(root, lower) {
					continue
				}
				// X1: auth/DELETE claims need code evidence (not test-green alone).
				if workflow.CriteriaRequiresAuthOrDeleteEvidence(lower) &&
					!workflow.CriteriaAuthDeleteEvidenceOK(root, lower) {
					continue
				}
				lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
				marked++
				continue
			}
			if n := workflow.SuccessCriteriaPhaseNum(lower); n > 0 {
				continue
			}
			if criteriaLooksLikeTestsPass(lower) {
				if criteriaLooksLikeCoverage(lower) {
					continue // W4: never auto-mark coverage claims
				}
				if testsEvidenceOK(root) {
					lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
					marked++
				}
				continue
			}
			if criteriaLooksLikeStructure(lower) {
				if workflow.PlanStructurePathsOK(root, lower) {
					lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
					marked++
				}
				continue
			}
			if criteriaLooksLikeHealth(lower) {
				if healthPayloadLooksOK(root) {
					lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
					marked++
				}
				continue
			}
			if strings.Contains(lower, "go build") || strings.Contains(lower, "go test") ||
				strings.Contains(lower, "pytest") || strings.Contains(lower, "compileall") {
				if criteriaLooksLikeCoverage(lower) {
					continue
				}
				if testsEvidenceOK(root) || strings.Contains(lower, "compileall") {
					lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
					marked++
				}
			}
		}
	}

	if marked > 0 {
		_ = os.WriteFile(planPath, []byte(strings.Join(lines, "\n")), 0644)
		_ = setActivePhaseStatusForEvidence(root, active)
	}
	return marked
}

// setActivePhaseStatusForEvidence keeps Active Phase in-progress while the
// project is still running, but must not revert a completed plan header (F92).
func setActivePhaseStatusForEvidence(root string, active int) error {
	planPath := filepath.Join(root, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	status := "in-progress"
	if strings.EqualFold(strings.TrimSpace(workflow.ParsePlanMeta(string(data)).Status), "completed") {
		status = "completed"
	}
	return workflow.SetPhaseStatus(root, active, status)
}

func criteriaLooksLikeTestsPass(lower string) bool {
	return strings.Contains(lower, "tests pass") ||
		strings.Contains(lower, "pytest") ||
		strings.Contains(lower, "all phase") && strings.Contains(lower, "test") ||
		(strings.Contains(lower, "test") && strings.Contains(lower, "pass"))
}

func criteriaLooksLikeCoverage(lower string) bool {
	return strings.Contains(lower, "coverage") ||
		(strings.Contains(lower, "%") && (strings.Contains(lower, "test") || strings.Contains(lower, "pytest")))
}

func criteriaLooksLikeStructure(lower string) bool {
	return strings.Contains(lower, "structure") ||
		strings.Contains(lower, "layered architecture") ||
		strings.Contains(lower, "project structure") ||
		strings.Contains(lower, "directory layout")
}

func criteriaLooksLikeHealth(lower string) bool {
	return strings.Contains(lower, "/health") || strings.Contains(lower, "health endpoint") ||
		strings.Contains(lower, "health check")
}

// testsEvidenceOK requires a tests/ tree (or *_test.go) — compile alone is not enough.
func testsEvidenceOK(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "tests")); err == nil {
		return dirNonEmpty(filepath.Join(root, "tests"))
	}
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			if info != nil && info.IsDir() {
				if projectfiles.ShouldSkipWalkDir(info.Name()) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") ||
			(strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func dirNonEmpty(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() {
			return true
		}
		if e.Name() != "__pycache__" {
			return true
		}
	}
	return false
}

// runGoBuildCheck returns true if "go build ./..." succeeds within a short timeout.
func runGoBuildCheck(wd string) bool {
	if !hasGoMod(wd) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = wd
	return cmd.Run() == nil
}

// runGoTestCheck returns true if "go test ./..." succeeds within a short timeout.
func runGoTestCheck(wd string) bool {
	if !hasGoMod(wd) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-timeout", "45s", "./...")
	cmd.Dir = wd
	return cmd.Run() == nil
}

func hasGoMod(wd string) bool {
	_, err := os.Stat(filepath.Join(wd, "go.mod"))
	return err == nil
}

// listGeneratedGoFiles returns Go files excluding .avatars/ and avatars/.
func listGeneratedGoFiles(root string) []string {
	// S3: Exclude directories by ".avatars/" prefix, not basename "avatars".
	// Previously "e.Name() != \"avatars\"" also excluded user projects
	// named "avatars" — a false positive.
	var files []string
	dirs := []string{root}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != ".avatars" {
			dirs = append(dirs, root+"/"+e.Name())
		}
	}
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				files = append(files, dir+"/"+e.Name())
			}
		}
	}
	return files
}

// updateArchitectureAfterBuild refreshes architecture.md after code generation.
// R3-9B: rewrites Entry/Layers/DataFlow/Dependencies from disk.
// R3-9A: status stays draft unless quality + health OK.
// R3-9C: heuristic RegPoints always; optional LLM enrich when still weak (non-blocking).
func updateArchitectureAfterBuild(root string, buildOK bool, client llm.Client, parentCtx context.Context) {
	doc, err := arch.ReadArchDoc(root)
	if err != nil || doc == nil {
		doc = arch.NewArchDoc("workflow-auto-update", root, nil)
		doc.Overview = "Project architecture (auto-updated after build)."
	}

	noHollow := hollowEntrypointCheck(root) == ""
	healthy := buildOK && noHollow
	_ = arch.RefreshArchDocFromProject(root, doc, arch.RefreshOptions{
		PreserveOverview: true,
		ProjectHealthy:   healthy,
		HealthKnown:      true,
		LLM:              client,
		Ctx:              parentCtx,
	})
	doc.Meta.Source = "workflow-auto-update"
	_ = arch.WriteArchDoc(root, doc)
}
