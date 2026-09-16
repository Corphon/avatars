package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"avatars/internal/llm"
	"avatars/internal/prompt"
)

// ComplexityLevel is the bucket a user request falls into.
type ComplexityLevel string

const (
	ComplexityTrivial ComplexityLevel = "trivial"
	ComplexitySmall   ComplexityLevel = "small"
	ComplexityMedium  ComplexityLevel = "medium"
	ComplexityLarge   ComplexityLevel = "large"
	// P1-3: Clarify is a new level for ambiguous requests that need
	// user clarification before proceeding. Not a complexity bucket
	// per se, but a routing signal.
	ComplexityClarify ComplexityLevel = "clarify"
)

// Intent captures the LLM's understanding of a user request before
// it's mapped to a complexity level. P1-3a: Unified intent struct.
type Intent struct {
	Action     string  `json:"action"`     // edit_file, create_script, bootstrap_project, ask_question, clarify
	Target     string  `json:"target"`     // file path, directory, project name, or empty
	Lang       string  `json:"lang"`       // go, py, js, rs, "" if unclear
	Mode       string  `json:"mode"`       // incremental, full, review_only
	Confidence float64 `json:"confidence"` // 0.0-1.0
}

// MapToComplexity maps an Intent to a ComplexityLevel based on action
// and confidence. Low-confidence intents route to clarify.
func (i Intent) MapToComplexity() ComplexityLevel {
	if i.Confidence < 0.6 {
		return ComplexityClarify
	}
	switch i.Action {
	case "ask_question", "clarify":
		return ComplexityTrivial
	case "edit_file":
		if i.Mode == "incremental" {
			return ComplexitySmall
		}
		return ComplexityMedium
	case "create_script":
		return ComplexityTrivial
	case "bootstrap_project":
		return ComplexityLarge
	case "review_only":
		return ComplexitySmall
	default:
		return ComplexityMedium
	}
}

// ComplexityAssessment is the result of the commander LLM call (or its
// heuristic fallback). The runtime uses Level to decide whether to
// bypass the multi-avatar workflow and dispatch the request as a
// direct script/edit. SuggestedAvatars / ParallelGroups let the
// runtime prune the plan for small/medium requests; for large they
// are ignored and the canonical 5-avatar chain is used.
type ComplexityAssessment struct {
	Level            ComplexityLevel
	SuggestedAvatars []string
	ParallelGroups   []int
	Reason           string
	FromLLM          bool
}

// IsTrivial is a convenience for `if assessment.IsTrivial() { ... }`.
func (a ComplexityAssessment) IsTrivial() bool {
	return a.Level == ComplexityTrivial
}

// IsBypassable returns true for trivial requests the runtime can
// dispatch as a single command (script/edit) without spinning up the
// full multi-avatar workflow.
func (a ComplexityAssessment) IsBypassable() bool {
	return a.IsTrivial()
}

// IsClarify returns true when the LLM determined the request needs
// user clarification before proceeding (confidence < 0.6).
func (a ComplexityAssessment) IsClarify() bool {
	return a.Level == ComplexityClarify
}

// ComplexityLLMFactory is set by the runtime (cmd/avatars) at startup
// to enable LLM-driven complexity assessment. When nil, Assess falls
// back to the deterministic keyword + length heuristic. The factory
// pattern is used (rather than passing the client through every call
// site) to keep the planner package decoupled from the LLM bootstrap
// path and to make pure-heuristic tests trivial.
var ComplexityLLMFactory func() (llm.Client, func(), error)

// Assess returns the complexity assessment for a user request. It
// first attempts the LLM path (if ComplexityLLMFactory is set and the
// call succeeds) and falls back to the heuristic on any failure. The
// fallback is intentionally always-on so the system stays functional
// when the LLM is unreachable.
func Assess(input string) ComplexityAssessment {
	// Hard pre-guard: multi-phase tasks are NEVER trivial — bypass LLM entirely.
	// The LLM complexity classifier consistently under-classifies structured
	// multi-phase tasks. Phase count ≥ 2 means at least small complexity.
	phaseCount := strings.Count(input, "## Phase") + strings.Count(input, "### Phase")
	if phaseCount >= 2 {
		level := ComplexityMedium
		if phaseCount >= 3 || len(strings.Fields(input)) > 80 {
			level = ComplexityLarge
		}
		return ComplexityAssessment{
			Level:            level,
			Reason:           fmt.Sprintf("multi-phase task (%d phases) — cannot be trivial", phaseCount),
			SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
			ParallelGroups:   []int{2, 2, 1},
			FromLLM:          false,
		}
	}

	if LooksLikeProgressReconcileTask(input) {
		return ComplexityAssessment{
			Level:            ComplexitySmall,
			Reason:           "checklist/progress reconcile — skip implement DAG",
			SuggestedAvatars: []string{"Planner", "Synthesizer"},
			ParallelGroups:   []int{1},
		}
	}

	// Continuation guard: "继续推进" / "接着做" with an active workflow plan
	// should NEVER be trivial. These imply multi-step continuation work.
	lowerInput := strings.ToLower(input)
	if isContinuationPhrase(lowerInput) && hasActiveWorkflowPlan() {
		return ComplexityAssessment{
			Level:            ComplexityLarge,
			Reason:           "continuation phrase with active plan — must use full pipeline with Critic",
			SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
			ParallelGroups:   []int{2, 2, 1},
		}
	}

	// NL smoke Batch1/A: active workflow + implement intent → medium+Builder.
	// Language-agnostic: phase docs + scaffold verbs, not Go-only.
	if looksLikeActivePhaseImplement(input) {
		return ComplexityAssessment{
			Level:            ComplexityMedium,
			Reason:           "active workflow phase implement — cannot be Direct trivial",
			SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
			ParallelGroups:   []int{2, 2},
		}
	}

	// Doc-only tasks route to Direct/DocEditor when the task is PURELY
	// about documentation. If code implementation verbs are present, do NOT bypass.
	// This prevents tasks like "Create a Python CLI tool (README.md)" from being
	// classified as doc-only due to the .md file mention.
	// Repository analysis that happens to name a markdown report is not doc-edit.
	if OnlyDocTargets(lowerInput) && !isCodeImplementationGuard(input) && !LooksLikeRepoAnalysisOrReport(input) {
		return ComplexityAssessment{
			Level:            ComplexityTrivial,
			Reason:           "documentation-only task — routing to Direct/DocEditor",
			SuggestedAvatars: []string{"Direct"},
			ParallelGroups:   []int{1},
		}
	}

	if ComplexityLLMFactory != nil {
		if client, cleanup, err := ComplexityLLMFactory(); err == nil && client != nil {
			if cleanup != nil {
				defer cleanup()
			}
			if assessment, ok := assessWithLLM(input, client); ok {
				// P4-5: Hard guard — analysis/search/statistics requests
				// are NEVER trivial, even if the LLM says so. They
				// require file reading and shell execution (grep/find/wc)
				// which the Direct avatar can't do.
				if assessment.IsTrivial() && isAnalysisComplexityGuard(input) {
					assessment = ComplexityAssessment{
						Level:            ComplexitySmall,
						SuggestedAvatars: []string{"Planner", "Builder", "Synthesizer"},
						ParallelGroups:   []int{2, 1},
						Reason:           "analysis/search request: requires file reading, cannot be trivial",
						FromLLM:          false,
					}
				}
				if assessment.IsTrivial() && isCodeImplementationGuard(input) {
					assessment = ComplexityAssessment{
						Level:            ComplexitySmall,
						SuggestedAvatars: []string{"Planner", "Builder", "Critic", "Synthesizer"},
						ParallelGroups:   []int{2, 1, 1},
						Reason:           "code-implementation: multi-file task cannot be trivial",
						FromLLM:          false,
					}
				}
				if assessment.IsTrivial() && looksLikeActivePhaseImplement(input) {
					assessment = ComplexityAssessment{
						Level:            ComplexityMedium,
						SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
						ParallelGroups:   []int{2, 2},
						Reason:           "active workflow phase implement — cannot be trivial",
						FromLLM:          false,
					}
				}
				return assessment
			}
		}
	}
	return heuristicAssess(input)
}

// heuristicAssess is the deterministic fallback. It is intentionally
// narrow: it favors "trivial" only when the request is short AND has
// no multi-step verb. Conservative classification is the safe failure
// mode — we do NOT want to spuriously bypass the 5-avatar chain.
func heuristicAssess(input string) ComplexityAssessment {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ComplexityAssessment{
			Level:            ComplexityTrivial,
			Reason:           "empty input",
			SuggestedAvatars: []string{"Direct"},
			ParallelGroups:   []int{1},
		}
	}
	lower := strings.ToLower(trimmed)
	wordCount := len(strings.Fields(trimmed))

	// Trivial: short request with no code/file action verb. Eight
	// words is a generous cap; the typical "用 python 写一个九九乘法表"
	// style request is 4-6 words.
	multiStepVerbs := []string{
		"refactor", "redesign", "migrate", "implement", "architect",
		"重构", "实现", "迁移", "全面", "设计", "升级", "拆分", "整合",
		"analyze this repository", "analyze the repository", "analyze the codebase", "analyze the project",
		"分析这个仓库", "分析这个项目", "梳理整个",
		"find", "search", "count", "list all", "grep", "scan for",
		"找出", "搜索", "统计", "查找", "列出所有", "扫描",
	}
	isMultiStep := containsAny(lower, multiStepVerbs) || containsAnyCJK(lower, multiStepVerbs)
	// P3-2: Code-implementation tasks should never be trivial.
	codeImplVerbs := []string{
		"add a new", "create a new", "implement a", "build a",
		"plugin", "handler", "module", "feature", "service",
		"添加新", "创建新", "实现", "构建",
		"插件", "处理器", "模块", "功能", "服务",
		// P8-1: standalone creation verbs — "创建一个新的" has "一个"
		// between "创建" and "新", so "创建新" never matches. Add
		// standalone verbs to catch all creation patterns.
		"创建", "新建", "生成", "写一个", "做一个",
		"create", "generate", "write a",
	}
	isCodeImpl := containsAny(lower, codeImplVerbs) || containsAnyCJK(lower, codeImplVerbs)

	// Documentation-only tasks (only .md/.yaml/.txt files) are not code implementation,
	// even if they use verbs like "create". Writing docs doesn't need go build.
	if isCodeImpl && OnlyDocTargets(lower) {
		isCodeImpl = false
	}
	// Keep classic one-shot Direct scripts (九九乘法表 / hello world) as trivial
	// unless scaffold / multi-file signals are present.
	if isCodeImpl && isSimpleOneShotScript(lower) && !isCodeImplementationGuard(trimmed) {
		isCodeImpl = false
	}
	// Doc-only tasks ALWAYS use Direct path — no need for multi-avatar pipeline.
	if OnlyDocTargets(lower) && !isMultiStep && !LooksLikeRepoAnalysisOrReport(trimmed) {
		return ComplexityAssessment{
			Level:            ComplexityTrivial,
			Reason:           "documentation-only task — routing to Direct/DocEditor",
			SuggestedAvatars: []string{"Direct"},
			ParallelGroups:   []int{1},
		}
	}

	// Technical depth and multi-component indicators override word count.
	hasDepth := hasTechnicalDepth(trimmed)
	isMultiComp := isMultiComponent(trimmed)
	if wordCount <= 8 && !isMultiStep && !isCodeImpl && !hasDepth && !isMultiComp {
		return ComplexityAssessment{
			Level:            ComplexityTrivial,
			Reason:           "short request with no multi-step verb",
			SuggestedAvatars: []string{"Direct"},
			ParallelGroups:   []int{1},
		}
	}

	// Large: long request or explicit redesign/migration phrasing.
	largeVerbs := []string{"redesign", "migrate", "architect", "全面重构", "迁移到", "重构整个"}
	if wordCount > 50 || containsAny(lower, largeVerbs) {
		return ComplexityAssessment{
			Level:            ComplexityLarge,
			Reason:           "long request or explicit redesign/migration verb",
			SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Critic", "Synthesizer"},
			ParallelGroups:   []int{2, 2, 1},
		}
	}
	if wordCount > 15 || isMultiStep || isCodeImpl {
		return ComplexityAssessment{
			Level:            ComplexityMedium,
			Reason:           "multi-step or code-implementation request with research/review benefit",
			SuggestedAvatars: []string{"Planner", "Researcher", "Builder", "Synthesizer"},
			ParallelGroups:   []int{2, 2},
		}
	}

	// Small: 9-15 words, no obvious research need.
	return ComplexityAssessment{
		Level:            ComplexitySmall,
		Reason:           "short multi-step request, builder-only is sufficient",
		SuggestedAvatars: []string{"Planner", "Builder", "Synthesizer"},
		ParallelGroups:   []int{2, 1},
	}
}

// isAnalysisComplexityGuard returns true when the input is an analysis/
// search/statistics request that needs file reading. Used to prevent
// the LLM complexity classifier from marking these as trivial.
func isAnalysisComplexityGuard(input string) bool {
	if LooksLikeRepoAnalysisOrReport(input) {
		return true
	}
	lowered := strings.ToLower(input)
	analysisVerbs := []string{
		"find", "search", "count", "list", "grep", "scan",
		"找出", "搜索", "统计", "查找", "列出", "扫描",
		"how many", "多少",
	}
	for _, v := range analysisVerbs {
		if strings.Contains(lowered, v) {
			return true
		}
	}
	return false
}

// isCodeImplementationGuard returns true when the input describes a code
// implementation task that requires multi-file generation. Used to prevent
// the LLM complexity classifier from marking these as trivial.
// Language-agnostic: covers Go/Python/JS/TS/Rust scaffold signals.
func isCodeImplementationGuard(input string) bool {
	lowered := strings.ToLower(input)
	// Strong scaffold / layout signals alone are enough (R1-style Phase 1 lists).
	scaffoldIndicators := []string{
		"go.mod", "go mod", "go build", "go test", "cmd/server", "cmd\\server",
		"internal/", "migrations", "cargo.toml", "cargo build", "cargo test",
		"package.json", "npm ", "pnpm ", "pyproject.toml", "requirements.txt",
		"pytest", "src/main", "__init__.py", "脚手架", "scaffold",
		"phase 1", "phase1", "phase 2", "phase2",
	}
	for _, ind := range scaffoldIndicators {
		if strings.Contains(lowered, ind) {
			return true
		}
	}
	// Strong code verbs that indicate non-trivial implementation.
	// Avoid bare "写" — it matches simple one-shot scripts ("写一个九九乘法表").
	codeVerbs := []string{
		"implement", "build", "create a ", "generate", "write a ",
		"实现", "构建", "创建", "生成", "编写", "开发", "搭建",
	}
	hasCodeVerb := containsAny(lowered, codeVerbs) || containsAnyCJK(lowered, codeVerbs)
	if !hasCodeVerb {
		return false
	}
	// Must also mention at least one code-specific concept.
	codeIndicators := []string{
		".py", ".go", ".rs", ".js", ".ts", "python", "golang", "rust",
		"cli", "sqlite", "database", "api", "package", "module", "library",
		"test", "app", "application", "server", "storage", "handler",
		"encrypt", "auth", "密码", "加密", "认证", "网络", "并发",
		"jwt", "crud", "middleware", "repository",
	}
	for _, ind := range codeIndicators {
		if strings.Contains(lowered, ind) {
			return true
		}
	}
	return false
}

// LooksLikeActivePhaseImplement is the exported form of looksLikeActivePhaseImplement
// for runtime resume/in-progress bumps.
func LooksLikeActivePhaseImplement(input string) bool {
	return looksLikeActivePhaseImplement(input)
}

// LooksLikeImplementOrResume reports implement / scaffold / resume intents
// used to bump Direct-trivial assessments. Language-agnostic.
func LooksLikeImplementOrResume(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	if LooksLikeProgressReconcileTask(input) {
		return false
	}
	if isContinuationPhrase(lower) || isCodeImplementationGuard(input) || looksLikeActivePhaseImplement(input) {
		return true
	}
	return looksLikePhaseGuidedCodeWork(lower)
}

// LooksLikeProgressReconcileTask reports "align the checklist / explain phases"
// work: markdown progress, not a new library implementation. Cross-language.
func LooksLikeProgressReconcileTask(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return false
	}
	if looksLikeRepairOrContinueTests(lower) && !looksLikeAlreadyGreenChecklistAlign(lower) {
		return false
	}
	signals := []string{
		"align the checklist", "align checklist", "tick the checklist",
		"checklist with", "real progress", "which phase", "phase division",
		"split phases", "explain phases", "progress report",
		"清单", "进度对齐", "划阶段", "第几阶段", "阶段划分", "勾成", "勾清单",
	}
	hit := false
	for _, s := range signals {
		if strings.Contains(lower, s) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	fresh := []string{
		"implement a", "create a library", "create a package", "scaffold",
		"write tests", "add a new", "from scratch",
		"写一个库", "从零", "搭脚手架",
	}
	for _, s := range fresh {
		if strings.Contains(lower, s) {
			return false
		}
	}
	return true
}

// looksLikeAlreadyGreenChecklistAlign is true when tests are already reported
// green and the ask is to sync the checklist — not to keep writing or repairing.
func looksLikeAlreadyGreenChecklistAlign(lower string) bool {
	green := strings.Contains(lower, "也绿") || strings.Contains(lower, "已经绿") ||
		strings.Contains(lower, "能测绿") || strings.Contains(lower, "测绿") ||
		strings.Contains(lower, "already green") || strings.Contains(lower, "already pass") ||
		strings.Contains(lower, "suite is green") || strings.Contains(lower, "tests are green") ||
		strings.Contains(lower, "tests pass")
	align := strings.Contains(lower, "清单") || strings.Contains(lower, "checklist") ||
		strings.Contains(lower, "进度对齐") || strings.Contains(lower, "real progress")
	return green && align
}

func looksLikeRepairOrContinueTests(lower string) bool {
	tokens := []string{
		"fix the failing", "make tests pass", "tests still fail", "still fails",
		"go test", "pytest", "npm test", "cargo test", "dotnet test", "mvn test",
		"write it out", "write the code", "write the library", "keep writing",
		"continue writing", "library code",
		"修绿", "修测", "没过门", "跑绿",
		"写出来", "接着写", "库代码", "还没看到",
		"假时钟", "fake clock", "injectable clock",
	}
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// looksLikeActivePhaseImplement is true when an active workflow plan exists
// and the utterance asks to implement / scaffold / continue building a phase.
// Language-agnostic: does not require Go-specific tokens.
func looksLikeActivePhaseImplement(input string) bool {
	if !hasActiveWorkflowPlan() {
		return false
	}
	// Inspect/report work is not a phase implement pass. Bare "write" would
	// otherwise match "write ana.md" after workflow bootstrap creates a plan.
	if LooksLikeRepoAnalysisOrReport(input) {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(input))
	implSignals := []string{
		"实现", "生成", "编写", "构建", "搭建", "脚手架", "scaffold",
		"implement", "generate", "build", "create", "write",
		"phase 1", "phase1", "phase 2", "go build", "go test",
		"cargo build", "npm test", "pytest", "go.mod", "cmd/server",
		"按 phase", "按phase", "phase1.md", "phase2.md",
	}
	return containsAny(lower, implSignals) || containsAnyCJK(lower, implSignals)
}

// LooksLikeRepoAnalysisOrReport is true for inspect/explain/report work whose
// deliverable may be markdown. Cross-language: not a code-implement pass.
func LooksLikeRepoAnalysisOrReport(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return false
	}
	if looksLikePhaseGuidedCodeWork(lower) {
		return false
	}
	fresh := []string{
		"implement a", "create a library", "create a package", "scaffold",
		"from scratch", "写一个库", "从零", "搭脚手架",
	}
	for _, s := range fresh {
		if strings.Contains(lower, s) {
			return false
		}
	}
	signals := []string{
		"analyze this repository", "analyze the current repository",
		"analyze the codebase", "analyze the project",
		"explain what it does", "report concrete issues", "find concrete issues",
		"propose a refactoring", "do not modify files",
		"summarize in ",
		"分析这个仓库", "分析这个项目", "梳理整个",
	}
	return containsAny(lower, signals) || containsAnyCJK(lower, signals)
}

// OnlyDocTargets returns true if the task only mentions documentation file types
// (.md, .yaml, .yml, .txt, .json) and no code file types (.go, .py, .js, etc.).
func OnlyDocTargets(lower string) bool {
	// Phase-guided implement mentioning phaseN.md is NOT doc-only.
	if isCodeImplementationGuard(lower) || looksLikePhaseGuidedCodeWork(lower) {
		return false
	}
	codeExts := []string{".go", ".py", ".js", ".ts", ".rs", ".java", ".cs", ".c ", ".cpp", ".rb", ".php", ".swift"}
	for _, ext := range codeExts {
		if strings.Contains(lower, ext) {
			return false
		}
	}
	// Also check for programming language name references (not just file extensions).
	// A task mentioning "Python" or "Rust" is a code task even without ".py" in the text.
	codeLangs := []string{"python", "golang", "rust", "javascript", "typescript", "c++", "c language", "node", "go "}
	for _, lang := range codeLangs {
		if strings.Contains(lower, lang) {
			return false
		}
	}
	// SQL/DB references also indicate code tasks.
	codeTech := []string{"sqlite", "database", "mysql", "postgres", "mongodb", "api", "cli"}
	for _, tech := range codeTech {
		if strings.Contains(lower, tech) {
			return false
		}
	}
	docExts := []string{".md", ".yaml", ".yml", ".txt"}
	for _, ext := range docExts {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	return false
}

// looksLikePhaseGuidedCodeWork detects "按 phase1.md 实现/搭脚手架" style intents
// where a .md path is a guide, not the deliverable.
func looksLikePhaseGuidedCodeWork(lower string) bool {
	hasPhaseDoc := strings.Contains(lower, "phase1.md") ||
		strings.Contains(lower, "phase2.md") ||
		strings.Contains(lower, "phase3.md") ||
		strings.Contains(lower, "docs/workflow") ||
		strings.Contains(lower, "avatars_plan")
	if !hasPhaseDoc {
		return false
	}
	workVerbs := []string{
		"实现", "生成", "构建", "搭建", "编写", "开发", "脚手架",
		"implement", "generate", "build", "scaffold", "create", "write",
	}
	return containsAny(lower, workVerbs) || containsAnyCJK(lower, workVerbs)
}

// isSimpleOneShotScript is true for short Direct-friendly script requests
// (any language): multiplication tables, hello world, one-file demos.
func isSimpleOneShotScript(lower string) bool {
	needles := []string{
		"九九", "乘法表", "hello world", "helloworld", "hello, world",
		"打印 hello", "print hello", "fizzbuzz", "猜数字",
	}
	return containsAny(lower, needles) || containsAnyCJK(lower, needles)
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// containsAnyCJK does flexible Chinese token matching using character bigrams.
// Unlike containsAny (exact substring), this handles partial matches:
// "创建一个新的" will match the pattern "创建新" because characters 创建→新
// appear in order with at most 3 chars between them.
// English patterns still use exact substring matching.
func containsAnyCJK(s string, patterns []string) bool {
	for _, pat := range patterns {
		if matchCJKFlexible(s, pat) {
			return true
		}
	}
	return false
}

// matchCJKFlexible matches a CJK pattern against text allowing up to maxGap
// characters between consecutive pattern characters.
func matchCJKFlexible(text, pattern string) bool {
	// For non-CJK patterns, fall back to exact substring.
	if !containsCJK(pattern) {
		return strings.Contains(text, pattern)
	}

	// For short patterns (<= 2 CJK chars), use exact match (too many false positives otherwise).
	cjkCount := countCJKChars(pattern)
	if cjkCount <= 2 {
		return strings.Contains(text, pattern)
	}

	// For longer patterns, allow gaps between consecutive CJK characters.
	// Extract CJK characters from pattern and match them in order in text.
	var patChars []rune
	for _, r := range pattern {
		if isCJK(r) {
			patChars = append(patChars, r)
		}
	}
	if len(patChars) < 3 {
		return strings.Contains(text, pattern)
	}

	textRunes := []rune(text)
	patIdx := 0
	const maxGap = 3 // max chars between consecutive CJK pattern chars
	for i := 0; i < len(textRunes) && patIdx < len(patChars); i++ {
		if textRunes[i] == patChars[patIdx] {
			patIdx++
		} else if patIdx > 0 {
			// Count gap since last match
			gap := 0
			j := i
			for ; j < len(textRunes) && gap <= maxGap && textRunes[j] != patChars[patIdx]; j++ {
				if isCJK(textRunes[j]) {
					gap++
				}
			}
			if j < len(textRunes) && textRunes[j] == patChars[patIdx] && gap <= maxGap {
				patIdx++
				i = j
			}
		}
	}
	return patIdx == len(patChars)
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Unified Ideographs Extension A
		(r >= 0x20000 && r <= 0x2A6DF) // CJK Unified Ideographs Extension B
}

func containsCJK(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}

func countCJKChars(s string) int {
	count := 0
	for _, r := range s {
		if isCJK(r) {
			count++
		}
	}
	return count
}

// hasTechnicalDepth detects indicators that a task requires non-trivial
// engineering effort (databases, encryption, networking, etc.).
func hasTechnicalDepth(input string) bool {
	lower := strings.ToLower(input)
	deepSignals := []string{
		// Data persistence
		"database", "sqlite", "postgres", "mysql", "mongodb", "redis",
		"数据库", "存储", "持久化",
		// Cryptography / Security
		"encrypt", "decrypt", "aes", "rsa", "hash", "password", "auth",
		"加密", "解密", "认证", "鉴权",
		// Networking / API
		"rest", "graphql", "grpc", "websocket", "http server", "api",
		"网络", "接口", "服务端",
		// Concurrency / Performance
		"concurrent", "parallel", "async", "thread", "goroutine",
		"并发", "异步", "线程", "协程",
		// Testing / Quality
		"unit test", "integration test", "test coverage",
		"单元测试", "集成测试", "测试覆盖",
		// Architecture
		"microservice", "plugin", "middleware", "pipeline",
		"微服务", "中间件", "流水线",
	}
	return containsAny(lower, deepSignals)
}

// isMultiComponent detects tasks that span multiple concerns
// (e.g., CLI + storage + testing).
func isMultiComponent(input string) bool {
	lower := strings.ToLower(input)
	components := [][]string{
		{"cli", "command", "命令行", "终端", "argparse", "cobra"},
		{"database", "sql", "storage", "sqlite", "数据库", "存储", "json file"},
		{"test", "测试", "pytest", "unittest"},
		{"web", "http", "api", "rest", "server", "网站", "网页"},
		{"config", "配置", "yaml", "toml", "env"},
		{"import", "export", "导入", "导出", "csv"},
	}
	count := 0
	for _, group := range components {
		if containsAny(lower, group) {
			count++
		}
	}
	return count >= 2
}

// complexitySpec is the JSON shape the commander LLM must return.
type complexitySpec struct {
	Intent           *intentSpec `json:"intent"`
	Level            string      `json:"level"`
	SuggestedAvatars []string    `json:"suggested_avatars"`
	ParallelGroups   []int       `json:"parallel_groups"`
	Reason           string      `json:"reason"`
}

type intentSpec struct {
	Action     string  `json:"action"`
	Target     string  `json:"target"`
	Lang       string  `json:"lang"`
	Mode       string  `json:"mode"`
	Confidence float64 `json:"confidence"`
}

func (is *intentSpec) toIntent() Intent {
	if is == nil {
		// No intent provided by LLM — default to moderate confidence.
		// The existing level field still drives classification.
		return Intent{Confidence: 0.8}
	}
	return Intent{
		Action:     strings.TrimSpace(is.Action),
		Target:     strings.TrimSpace(is.Target),
		Lang:       strings.TrimSpace(is.Lang),
		Mode:       strings.TrimSpace(is.Mode),
		Confidence: is.Confidence,
	}
}

func assessWithLLM(input string, client llm.Client) (ComplexityAssessment, bool) {
	prompt := complexitySystemPrompt()
	llmCtx, llmCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer llmCancel()
	response, err := client.Generate(llmCtx, llm.Request{
		SystemPrompt:     prompt,
		UserPrompt:       "User request:\n" + strings.TrimSpace(input),
		StructuredOutput: true,
		JSONSchema:       llm.ComplexityJSONSchema,
	})
	if err != nil || response.Fallback {
		return ComplexityAssessment{}, false
	}
	spec, ok := parseComplexitySpec(response.Text)
	if !ok {
		return ComplexityAssessment{}, false
	}
	// P1-3b: Two-layer classification. When LLM provides Intent with
	// explicit action, use Intent→Complexity mapping. Otherwise use raw level.
	intent := spec.Intent.toIntent()
	level := ComplexityLevel(strings.ToLower(strings.TrimSpace(spec.Level)))
	if spec.Intent != nil && intent.Action != "" {
		mappedLevel := intent.MapToComplexity()
		if mappedLevel == ComplexityClarify || level == "" {
			level = mappedLevel
		}
	}
	switch level {
	case ComplexityTrivial, ComplexitySmall, ComplexityMedium, ComplexityLarge, ComplexityClarify:
		// ok
	default:
		return ComplexityAssessment{}, false
	}
	return ComplexityAssessment{
		Level:            level,
		SuggestedAvatars: spec.SuggestedAvatars,
		ParallelGroups:   spec.ParallelGroups,
		Reason:           spec.Reason,
		FromLLM:          true,
	}, true
}

func complexitySystemPrompt() string {
	return prompt.PlannerStablePrefix + "\n\n" + `Classify one user request. Return compact JSON only:
{"intent":{"action":"edit_file|create_script|bootstrap_project|ask_question|clarify","target":"","lang":"go|py|js|rs|","mode":"incremental|full|review_only","confidence":0.85},"level":"trivial|small|medium|large|clarify","suggested_avatars":[],"parallel_groups":[],"reason":""}

Intent actions:
- edit_file: modify/update/fix/refactor existing files. mode=incremental for small targeted changes, full for large rewrites.
- create_script: write one standalone script/program file. lang=py for Python, lang=go for Go, etc.
- bootstrap_project: scaffold a new multi-file project with structure.
- ask_question: the user is asking about the project, capabilities, or status — not requesting code changes.
- clarify: the request is genuinely ambiguous; ask the user one specific question.

Complexity levels:
- trivial: one-shot, single file, no research/survey/review needed. Direct avatar only.
- small: 1-2 steps, well-defined target. Planner+Builder+Synthesizer.
- medium: multi-step with research benefit. Planner+Researcher+Builder+Synthesizer.
- large: open-ended refactor, redesign, migration. Full 5-avatar chain with Critic.
- clarify: intent confidence < 0.6 or genuinely ambiguous request → ask user to clarify.

P1-3e Few-shot examples:
1. "用 python 写一个九九乘法表" → intent:{action:create_script, target:"", lang:py, mode:full, confidence:0.92}, level:trivial, reason:"single-file Python script"
2. "把 app.go 第42行的 timeout 从 30 改成 60" → intent:{action:edit_file, target:"app.go", lang:go, mode:incremental, confidence:0.95}, level:trivial, reason:"targeted single-line edit"
3. "创建一个 Go REST API 项目，包含 user 和 order 两个模块" → intent:{action:bootstrap_project, target:"go-rest-api", lang:go, mode:full, confidence:0.88}, level:large, reason:"multi-module project scaffold"
4. "分析这个项目的代码结构" → intent:{action:ask_question, target:"", lang:"", mode:review_only, confidence:0.90}, level:small, reason:"analysis request with file reading"
5. "帮我看一下" → intent:{action:clarify, target:"", lang:"", mode:"", confidence:0.30}, level:clarify, reason:"ambiguous — no clear action or target"
6. "重构整个认证模块，从 session 迁移到 JWT" → intent:{action:edit_file, target:"auth/", lang:go, mode:full, confidence:0.85}, level:large, reason:"cross-file refactor with architectural changes"

NEVER use trivial for analysis/find/search/count/list/grep — those are at least small.
suggested_avatars: {Planner, Researcher, Builder, Critic, Synthesizer, Direct}. Direct ONLY for trivial one-shot actions.
parallel_groups: integers summing to len(suggested_avatars).
reason: one short sentence.`
}

func parseComplexitySpec(text string) (complexitySpec, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return complexitySpec{}, false
	}
	// P1-1: With json_schema + strict, the LLM returns clean JSON directly.
	// Try direct parse first, then fall back to fence-stripping for older models.
	var spec complexitySpec
	if err := json.Unmarshal([]byte(raw), &spec); err == nil && spec.Level != "" {
		return spec, true
	}
	// Fallback: strip markdown fences and find JSON block.
	if strings.HasPrefix(raw, "```") {
		if idx := strings.Index(raw, "\n"); idx >= 0 {
			raw = raw[idx+1:]
		}
		if last := strings.LastIndex(raw, "```"); last >= 0 {
			raw = raw[:last]
		}
		raw = strings.TrimSpace(raw)
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		raw = raw[start : end+1]
	}
	re := regexp.MustCompile(`,(\s*[}\]])`)
	raw = re.ReplaceAllString(raw, "$1")
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return complexitySpec{}, false
	}
	if strings.TrimSpace(spec.Level) == "" {
		return complexitySpec{}, false
	}
	return spec, true
}

// BuildForComplexity returns a Plan whose avatar set is sized to the
// assessment. For trivial, the plan has a single "Direct" avatar and a
// single workflow node — the runtime is expected to detect this and
// bypass the multi-avatar workflow, dispatching the request as a
// direct command instead. For small/medium, only the suggested
// avatars are included. For large, the canonical 5-avatar chain is
// returned (identical to BuildWithContext).
func BuildForComplexity(task string, context Context, assessment ComplexityAssessment) Plan {
	// Checklist-align is markdown progress, not codegen. SuggestedAvatars
	// alone is ignored for non-LLM small plans, which always injected Builder.
	if LooksLikeProgressReconcileTask(task) {
		if assessment.Level == "" {
			assessment.Level = ComplexitySmall
		}
		return sizedPlan(task, context, assessment, []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "update workflow checklists to match evidence on disk"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "explain phase progress to the user"},
		}, nil)
	}
	switch assessment.Level {
	case ComplexityTrivial:
		return trivialPlan(task)
	case ComplexitySmall:
		// P15: Code-implementation tasks MUST include Critic to verify Builder output.
		// Without Critic, Builder can silently produce 0 files and Synthesizer reports "completed".
		// This check must come BEFORE dynamicAvatars to ensure Critic is always included.
		codeVerbs := []string{"create", "build", "implement", "generate", "write code", "add function",
			"创建", "实现", "生成", "编写"}
		if containsAny(strings.ToLower(task), codeVerbs) {
			return sizedPlan(task, context, assessment, []Avatar{
				{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and assign workflow nodes"},
				{ID: "avatar-builder", Role: "Builder", Responsibility: "propose the smallest viable implementation path"},
				{ID: "avatar-critic", Role: "Critic", Responsibility: "verify Builder output and flag gaps"},
				{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
			}, []WorkflowNode{
				{ID: "node-build", Title: "Shape the smallest viable change", AssignedRole: "Builder"},
				{ID: "node-review", Title: "Verify implementation completeness", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
			})
		}
		return sizedPlan(task, context, assessment, []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task into a single concrete action"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "produce the bounded output"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "format the result for the user"},
		}, []WorkflowNode{
			{ID: "node-build", Title: "Produce the bounded output", AssignedRole: "Builder"},
		})
	case ComplexityMedium:
		// P3-2: For code-implementation tasks, fan out survey into
		// parallel branches first. Takes precedence over LLM suggestions.
		if looksLikeTaskBenefitsFromParallelSurvey(task) {
			if plan, ok := parallelSurveyPlan(task, context, assessment, true); ok {
				return plan
			}
			// Compact/empty trees skip the 3-way fan-out but still keep Critic.
			return mediumPlanWithCritic(task, context, assessment)
		}
		// P9-3: deterministic assessments (multi-phase / resume bump) set Critic in
		// SuggestedAvatars with FromLLM=false — dynamicAvatars would ignore them.
		if suggestedIncludesRole(assessment, "Critic") || needsCriticForMediumTask(task) {
			if plan, ok := parallelSurveyPlan(task, context, assessment, true); ok {
				return plan
			}
			return mediumPlanWithCritic(task, context, assessment)
		}
		if avatars, nodes, ok := dynamicAvatars(assessment); ok {
			return sizedPlan(task, context, assessment, avatars, nodes)
		}
		return sizedPlan(task, context, assessment, []Avatar{
			{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and set checkpoints"},
			{ID: "avatar-researcher", Role: "Researcher", Responsibility: "survey repository context and collect facts"},
			{ID: "avatar-builder", Role: "Builder", Responsibility: "shape the bounded change"},
			{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
		}, []WorkflowNode{
			{ID: "node-survey", Title: "Survey current repository context", AssignedRole: "Researcher"},
			{ID: "node-build", Title: "Shape the bounded change", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
		})
	default:
		// P3-2: For Large tasks, fan out survey into parallel branches.
		// Takes precedence over LLM-suggested avatars.
		if looksLikeTaskBenefitsFromParallelSurvey(task) {
			if plan, ok := parallelSurveyPlan(task, context, assessment, true); ok {
				return plan
			}
			return mediumPlanWithCritic(task, context, assessment)
		}
		if avatars, nodes, ok := dynamicAvatars(assessment); ok {
			return sizedPlan(task, context, assessment, avatars, nodes)
		}
		return BuildWithContext(task, context)
	}
}

func looksLikeTaskBenefitsFromParallelSurvey(task string) bool {
	if isNearlyEmptyProject(".") {
		return false
	}
	lowered := strings.ToLower(task)
	indicators := []string{
		"implement", "refactor", "migrate", "add", "build", "scaffold",
		"实现", "重构", "迁移", "添加", "构建", "搭建", "搭一个", "从零", "脚手架", "编写", "开发",
		"plugin", "feature", "module",
		"插件", "功能", "模块",
	}
	for _, indicator := range indicators {
		if strings.Contains(lowered, indicator) {
			return true
		}
	}
	return false
}

func suggestedIncludesRole(assessment ComplexityAssessment, role string) bool {
	want := strings.ToLower(strings.TrimSpace(role))
	for _, a := range assessment.SuggestedAvatars {
		if strings.ToLower(strings.TrimSpace(a)) == want {
			return true
		}
	}
	return false
}

// needsCriticForMediumTask forces Critic into medium DAGs for multi-phase /
// greenfield implement work even when SuggestedAvatars omitted it (P9-3).
func needsCriticForMediumTask(task string) bool {
	if looksLikeActivePhaseImplement(task) || isCodeImplementationGuard(task) || LooksLikeImplementOrResume(task) {
		return true
	}
	lower := strings.ToLower(task)
	if strings.Contains(lower, "refactor") || strings.Contains(task, "重构") {
		return true
	}
	if strings.Count(task, "## Phase")+strings.Count(task, "### Phase") >= 2 {
		return true
	}
	// Explicit Phase 1 + Phase 2 without markdown headers (NL prompts).
	hasP1 := strings.Contains(lower, "phase 1") || strings.Contains(task, "阶段 1") || strings.Contains(task, "Phase 1")
	hasP2 := strings.Contains(lower, "phase 2") || strings.Contains(task, "阶段 2") || strings.Contains(task, "Phase 2")
	return hasP1 && hasP2
}

func mediumPlanWithCritic(task string, context Context, assessment ComplexityAssessment) Plan {
	return sizedPlan(task, context, assessment, []Avatar{
		{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and set checkpoints"},
		{ID: "avatar-researcher", Role: "Researcher", Responsibility: "survey repository context and collect facts"},
		{ID: "avatar-builder", Role: "Builder", Responsibility: "shape the bounded change"},
		{ID: "avatar-critic", Role: "Critic", Responsibility: "verify Builder output and flag gaps"},
		{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
	}, []WorkflowNode{
		{ID: "node-survey", Title: "Survey current repository context", AssignedRole: "Researcher"},
		{ID: "node-build", Title: "Shape the bounded change", AssignedRole: "Builder", DependsOn: []string{"node-survey"}},
		{ID: "node-review", Title: "Verify implementation completeness", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
	})
}

// parallelSurveyPlan creates a plan with parallel survey branches.
// Instead of a single Researcher reading all files sequentially,
// 3 Researcher nodes survey different file buckets concurrently.
//
// Plan structure (with Critic):
//
//	                 ┌─ Researcher(docs+manifests) ─┐
//	(Planner lifecycle)┼─ Researcher(source+logic)   ─┼── Builder ── Critic
//	                 └─ Researcher(config+ci)       ─┘
//
// Synthesizer remains a tail lifecycle after the DAG, not a scheduler slot.
func parallelSurveyPlan(task string, context Context, assessment ComplexityAssessment, includeCritic bool) (Plan, bool) {
	// Compact trees skip three survey LLM calls. Empty trees stay gated by
	// looksLikeTaskBenefitsFromParallelSurvey so engine tests that pass
	// includeCritic on an empty cwd can still request the 3-way DAG.
	if isCompactSourceTree(".") {
		return Plan{}, false
	}
	title := strings.TrimSpace(task)
	if title == "" {
		title = "Untitled task"
	}
	reason := assessment.Reason
	if reason == "" {
		reason = "Parallel survey plan for concurrent file analysis."
	}

	avatars := []Avatar{
		{ID: "avatar-planner", Role: "Planner", Responsibility: "decompose the task and assign parallel survey nodes"},
		{ID: "avatar-researcher-docs", Role: "Researcher", InstanceID: "docs", Responsibility: "survey documentation and manifest files"},
		{ID: "avatar-researcher-src", Role: "Researcher", InstanceID: "src", Responsibility: "survey source code and domain logic"},
		{ID: "avatar-researcher-cfg", Role: "Researcher", InstanceID: "cfg", Responsibility: "survey configuration and CI files"},
		{ID: "avatar-builder", Role: "Builder", Responsibility: "shape the bounded change from merged survey results"},
		{ID: "avatar-synthesizer", Role: "Synthesizer", Responsibility: "merge validated outputs into a user-ready summary"},
	}

	nodes := []WorkflowNode{
		{ID: "node-survey-docs", Title: "Survey docs and manifests", AssignedRole: "Researcher"},
		{ID: "node-survey-src", Title: "Survey source code", AssignedRole: "Researcher"},
		{ID: "node-survey-cfg", Title: "Survey config and CI", AssignedRole: "Researcher"},
		{ID: "node-build", Title: "Shape the bounded change", AssignedRole: "Builder",
			DependsOn: []string{"node-survey-docs", "node-survey-src", "node-survey-cfg"}},
	}

	if includeCritic {
		avatars = append(avatars, Avatar{ID: "avatar-critic", Role: "Critic", Responsibility: "challenge assumptions and capture risks"})
		nodes = append(nodes,
			WorkflowNode{ID: "node-review", Title: "Review risks and challenge gaps", AssignedRole: "Critic", DependsOn: []string{"node-build"}},
		)
	}

	return sizedPlan(task, context, assessment, avatars, nodes), true
}

// dynamicAvatars converts the commander LLM's suggested avatar roles into
// concrete Avatar and WorkflowNode slices. Returns ok=false when the
// assessment was not LLM-driven or the suggestions are empty.
func dynamicAvatars(assessment ComplexityAssessment) ([]Avatar, []WorkflowNode, bool) {
	if !assessment.FromLLM || len(assessment.SuggestedAvatars) == 0 {
		return nil, nil, false
	}
	avatars := make([]Avatar, 0, len(assessment.SuggestedAvatars))
	nodes := make([]WorkflowNode, 0, len(assessment.SuggestedAvatars))
	for i, role := range assessment.SuggestedAvatars {
		role = strings.TrimSpace(role)
		if role == "" || role == "Direct" {
			continue
		}
		id := "avatar-" + strings.ToLower(role)
		resp := avatarResponsibility(role)
		avatars = append(avatars, Avatar{ID: id, Role: role, Responsibility: resp})
		nodeID := "node-" + strings.ToLower(role)
		node := WorkflowNode{ID: nodeID, Title: avatarNodeTitle(role), AssignedRole: role}
		if i > 0 {
			prevRole := strings.TrimSpace(assessment.SuggestedAvatars[i-1])
			if prevRole != "" && prevRole != "Direct" {
				node.DependsOn = []string{"node-" + strings.ToLower(prevRole)}
			}
		}
		nodes = append(nodes, node)
	}
	if len(avatars) == 0 {
		return nil, nil, false
	}
	return avatars, nodes, true
}

func avatarResponsibility(role string) string {
	switch role {
	case "Planner":
		return "decompose the task and assign workflow nodes"
	case "Researcher":
		return "survey repository context and collect facts"
	case "Builder":
		return "propose the smallest viable implementation path"
	case "Critic":
		return "challenge assumptions and capture risks"
	case "Synthesizer":
		return "merge validated outputs into a user-ready summary"
	case "Runner":
		return "execute shell commands and verified mutations"
	default:
		return "execute the assigned role"
	}
}

func avatarNodeTitle(role string) string {
	switch role {
	case "Planner":
		return "Decompose task and set checkpoints"
	case "Researcher":
		return "Survey current repository context"
	case "Builder":
		return "Shape the smallest viable change"
	case "Critic":
		return "Review risks and challenge gaps"
	case "Synthesizer":
		return "Summarize outcome for the user"
	case "Runner":
		return "Execute shell and verified mutations"
	default:
		return role + " node"
	}
}

func trivialPlan(task string) Plan {
	title := strings.TrimSpace(task)
	if title == "" {
		title = "Untitled trivial task"
	}
	return Plan{
		Title:   title,
		Summary: "Trivial request: dispatch as a direct command without multi-avatar workflow.",
		Avatars: []Avatar{
			{ID: "avatar-direct", Role: "Direct", Responsibility: "execute the single concrete action"},
		},
		Nodes: []WorkflowNode{
			{ID: "node-direct", Title: "Execute direct action", AssignedRole: "Direct"},
		},
	}
}

func sizedPlan(task string, context Context, assessment ComplexityAssessment, avatars []Avatar, nodes []WorkflowNode) Plan {
	title := strings.TrimSpace(task)
	if title == "" {
		title = "Untitled task"
	}
	reason := assessment.Reason
	if reason == "" {
		reason = "Sized plan selected by complexity assessment."
	}
	plan := Plan{
		Title:   title,
		Summary: "Sized plan (" + string(assessment.Level) + "): " + reason,
		Avatars: avatars,
		Nodes:   nodes,
	}
	if context.Remediation != nil {
		trimmed := NormalizeRemediationEnvelope(*context.Remediation)
		if !trimmed.isEmpty() {
			plan.Remediation = &trimmed
		}
	}
	if context.Feedback != "" {
		plan.Feedback = context.Feedback
	}
	if context.ArchSummary != "" {
		plan.ArchSummary = context.ArchSummary
	}
	// P12 / S4.4: Per-file Builder decomposition — parallel by default.
	// Mid Python NL PY3: on nearly-empty greenfield, keep a single Builder so
	// scaffold files share one stack decision (sync vs async, one Base, etc.).
	if files := extractFileTargets(task); len(files) > 1 && !isNearlyEmptyProject(".") {
		files = sanitizeSplitTargets(files)
		if len(files) > 1 {
			plan.Nodes = splitBuilderNodes(plan.Nodes, files)
			plan.Summary += fmt.Sprintf(" | split into %d file-level Builder nodes (parallel)", len(files))
		} else if len(files) == 1 {
			plan.Summary += " | sanitized to single Builder target after layout filter"
		}
	} else if files := extractFileTargets(task); len(files) > 1 && isNearlyEmptyProject(".") {
		plan.Summary += " | greenfield: keep single Builder (no file-level split)"
	}
	// S4.4: apply ParallelGroups from complexity assessment when present.
	if len(assessment.ParallelGroups) > 0 {
		plan.Nodes = applyParallelGroups(plan.Nodes, assessment.ParallelGroups)
		plan.Summary += fmt.Sprintf(" | parallel_groups=%v", assessment.ParallelGroups)
	}
	plan.Nodes = WithoutCeremonialDAGNodes(plan.Nodes)
	return plan
}

// applyParallelGroups clears serial DependsOn chains within each group of
// consecutive same-role nodes so the scheduler can run them concurrently.
// Groups are sizes (e.g. [3] means first 3 eligible siblings share a frontier).
func applyParallelGroups(nodes []WorkflowNode, groups []int) []WorkflowNode {
	if len(nodes) == 0 || len(groups) == 0 {
		return nodes
	}
	out := append([]WorkflowNode(nil), nodes...)
	// Find Builder (or Researcher) runs that are currently serial-chained and
	// flatten the first group-sized batch to share the first node's DependsOn.
	i := 0
	for _, g := range groups {
		if g <= 1 {
			continue
		}
		for i < len(out) && out[i].AssignedRole != "Builder" && out[i].AssignedRole != "Researcher" {
			i++
		}
		if i >= len(out) {
			break
		}
		end := i + g
		if end > len(out) {
			end = len(out)
		}
		// Only flatten contiguous same-role nodes.
		role := out[i].AssignedRole
		sharedDeps := append([]string(nil), out[i].DependsOn...)
		for j := i; j < end; j++ {
			if out[j].AssignedRole != role {
				end = j
				break
			}
		}
		for j := i + 1; j < end; j++ {
			// Drop dependency on previous sibling — keep shared frontier.
			filtered := make([]string, 0, len(out[j].DependsOn))
			for _, dep := range out[j].DependsOn {
				isSibling := false
				for k := i; k < end; k++ {
					if dep == out[k].ID {
						isSibling = true
						break
					}
				}
				if !isSibling {
					filtered = append(filtered, dep)
				}
			}
			if len(filtered) == 0 {
				filtered = append([]string(nil), sharedDeps...)
			}
			out[j].DependsOn = filtered
		}
		i = end
	}
	return out
}

// isContinuationPhrase returns true if the input is a short continuation command
// like "继续推进", "接着做", "继续完成" — phrases that should trigger the full
// run pipeline rather than a trivial single action.
func isContinuationPhrase(input string) bool {
	trimmed := strings.TrimSpace(input)
	lower := strings.ToLower(trimmed)
	exactOrPrefix := []string{
		"继续推进", "接着做", "继续完成", "继续完善", "接着完善",
		"继续开发", "继续写", "继续", "接着来", "继续执行",
		"继续构建", "继续实现",
		"continue", "proceed", "go on", "resume",
	}
	for _, phrase := range exactOrPrefix {
		p := strings.ToLower(phrase)
		if lower == p || strings.HasPrefix(lower, p+" ") || strings.HasPrefix(trimmed, phrase) {
			return true
		}
	}
	// Mid-utterance recovery / resume (R2-style).
	midSignals := []string{"从中断", "被中断", "中断处", "resume from", "continue from"}
	for _, sig := range midSignals {
		if strings.Contains(lower, strings.ToLower(sig)) {
			return true
		}
	}
	return false
}

// hasActiveWorkflowPlan checks whether the current working directory has an
// active workflow plan with at least one phase. Used to determine whether
// continuation phrases should trigger the full pipeline.
func hasActiveWorkflowPlan() bool {
	planPath := filepath.Join("docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "### Phase") &&
		strings.Contains(string(data), "Active Phase")
}
