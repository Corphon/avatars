package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"avatars/internal/projectfiles"
)

type explorationRound struct {
	Title   string
	Targets []string
	Reason  string
}

func explorationRoundStartedSummary(index int, round explorationRound) string {
	parts := []string{fmt.Sprintf("Researcher round %d: %s", index+1, strings.TrimSpace(round.Title))}
	if targetSummary := summarizeExplorationTargets(round.Targets, 4); targetSummary != "" {
		parts = append(parts, "targets: "+targetSummary)
	}
	if reason := strings.TrimSpace(round.Reason); reason != "" {
		parts = append(parts, "reason: "+reason)
	}
	return strings.Join(parts, " | ") + "."
}

func explorationRoundCompletedSummary(index int, round explorationRound) string {
	title := strings.TrimSpace(round.Title)
	if title == "" {
		title = "unnamed round"
	}
	return fmt.Sprintf("Researcher round %d complete: %s (%d file(s)).", index+1, title, len(round.Targets))
}

func summarizeExplorationTargets(targets []string, limit int) string {
	if limit <= 0 {
		limit = 1
	}
	cleaned := make([]string, 0, len(targets))
	for _, target := range targets {
		trimmed := strings.TrimSpace(target)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, filepath.ToSlash(trimmed))
	}
	if len(cleaned) == 0 {
		return ""
	}
	if len(cleaned) <= limit {
		return strings.Join(cleaned, ", ")
	}
	return strings.Join(cleaned[:limit], ", ") + fmt.Sprintf(", +%d more", len(cleaned)-limit)
}

func buildExplorationRounds(input string, readTargets []string) []explorationRound {
	if len(readTargets) == 0 {
		return []explorationRound{{
			Title:   "bootstrap scan",
			Targets: projectfiles.BootstrapSurveyTargets(),
			Reason:  "no repository-specific evidence found yet",
		}}
	}
	explicit := []string{}
	discovery := []string{}
	for _, target := range readTargets {
		if isExplicitRequestTarget(input, target) {
			explicit = append(explicit, target)
			continue
		}
		discovery = append(discovery, target)
	}
	rounds := []explorationRound{}
	if len(explicit) > 0 {
		rounds = append(rounds, explorationRound{
			Title:   "explicit request targets",
			Targets: append([]string(nil), explicit...),
			Reason:  "user named these files directly",
		})
	}
	for _, bucket := range surveyPriorityBuckets() {
		bucketTargets := []string{}
		for _, target := range discovery {
			if surveyPriorityBucketForPath(target) == bucket.Key {
				bucketTargets = append(bucketTargets, target)
			}
		}
		if len(bucketTargets) == 0 {
			continue
		}
		rounds = append(rounds, explorationRound{
			Title:   bucket.Title,
			Targets: bucketTargets,
			Reason:  bucket.Reason,
		})
	}
	return rounds
}

func isExplicitRequestTarget(input string, target string) bool {
	lowered := strings.ToLower(input)
	normalizedTarget := strings.ToLower(filepath.ToSlash(strings.TrimSpace(target)))
	if normalizedTarget == "" {
		return false
	}
	return strings.Contains(lowered, normalizedTarget) || strings.Contains(lowered, strings.ToLower(filepath.Base(normalizedTarget)))
}

type surveyPriorityBucket struct {
	Key    string
	Title  string
	Reason string
}

func surveyPriorityBuckets() []surveyPriorityBucket {
	return []surveyPriorityBucket{
		{Key: "docs", Title: "project documentation", Reason: "understand declared purpose, user workflows, setup, and constraints"},
		{Key: "manifests", Title: "manifests and dependencies", Reason: "inspect language stack, dependency surface, and build commands"},
		{Key: "entrypoints", Title: "entrypoints", Reason: "trace how the project starts and wires major components"},
		{Key: "domain-logic", Title: "domain logic", Reason: "inspect core business behavior and risk-bearing implementation files"},
		{Key: "tests", Title: "tests", Reason: "inspect expected behavior, coverage boundaries, and regression signals"},
		{Key: "config-ci", Title: "config and CI", Reason: "inspect runtime configuration, deployment workflows, and operational assumptions"},
		{Key: "supplemental", Title: "supplemental evidence", Reason: "fill remaining context with lower-priority supporting files"},
	}
}

func surveyPriorityBucketForPath(path string) string {
	cleaned := filepath.Clean(path)
	base := strings.ToLower(filepath.Base(cleaned))
	ext := strings.ToLower(filepath.Ext(cleaned))
	lowered := strings.ToLower(cleaned)
	if isSurveyDocFile(cleaned) {
		return "docs"
	}
	if isSurveyManifestFile(cleaned) {
		return "manifests"
	}
	if isSurveyTestFile(cleaned) {
		return "tests"
	}
	switch base {
	case "main.go", "app.go", "main.py", "app.py", "server.py", "index.js", "index.ts", "app.js", "app.ts", "server.js", "server.ts":
		return "entrypoints"
	}
	for _, part := range strings.Split(lowered, string(filepath.Separator)) {
		switch part {
		case "cmd", "app", "server":
			if isSurveySourceFile(cleaned) {
				return "entrypoints"
			}
		case "config", "configs", ".github":
			return "config-ci"
		case "test", "tests", "__tests__":
			if isSurveySourceFile(cleaned) || isSurveyConfigFile(cleaned) {
				return "tests"
			}
		case "internal", "pkg", "src", "frontend", "web", "ui":
			if isSurveySourceFile(cleaned) {
				return "domain-logic"
			}
		}
	}
	if isSurveyTestFile(cleaned) {
		return "tests"
	}
	if isSurveyConfigFile(cleaned) || ext == ".sql" {
		return "config-ci"
	}
	if isSurveySourceFile(cleaned) {
		return "domain-logic"
	}
	return "supplemental"
}

// --- P3-2: Parallel Survey Node Support ---
// When the Planner creates parallel survey branches, each Researcher
// node is assigned a specific file bucket. The scheduler runs them
// concurrently via goroutines.

// isParallelSurveyNode returns true if this node is part of a
// parallel survey plan (node-survey-docs, node-survey-src, node-survey-cfg).
func isParallelSurveyNode(nodeID string) bool {
	switch strings.TrimSpace(nodeID) {
	case "node-survey-docs", "node-survey-src", "node-survey-cfg":
		return true
	}
	return false
}

// parallelSurveyBucket returns the file bucket a parallel survey node
// is responsible for.
func parallelSurveyBucket(nodeID string) string {
	switch strings.TrimSpace(nodeID) {
	case "node-survey-docs":
		return "docs"
	case "node-survey-src":
		return "source"
	case "node-survey-cfg":
		return "config"
	}
	return ""
}

// parallelSurveyBucketTargets maps each parallel survey bucket to the
// survey buckets it should read.
var parallelSurveyBucketTargets = map[string][]string{
	"docs":   {"docs", "manifests"},
	"source": {"entrypoints", "domain-logic", "supplemental"},
	"config": {"config-ci", "tests"},
}

// filterReadTargetsForParallelNode filters the full read target list
// to only include files that belong to this node's survey bucket.
func filterReadTargetsForParallelNode(nodeID string, targets []string) []string {
	bucket := parallelSurveyBucket(nodeID)
	if bucket == "" {
		return targets
	}
	allowed := parallelSurveyBucketTargets[bucket]
	if len(allowed) == 0 {
		return targets
	}
	filtered := make([]string, 0, len(targets))
	for _, target := range targets {
		targetBucket := surveyPriorityBucketForPath(target)
		for _, a := range allowed {
			if targetBucket == a {
				filtered = append(filtered, target)
				break
			}
		}
	}
	return filtered
}

// filterExplorationRoundsForParallelNode filters exploration rounds
// to only include those relevant to this node's survey bucket.
func filterExplorationRoundsForParallelNode(nodeID string, rounds []explorationRound) []explorationRound {
	bucket := parallelSurveyBucket(nodeID)
	if bucket == "" {
		return rounds
	}
	allowed := parallelSurveyBucketTargets[bucket]
	if len(allowed) == 0 {
		return rounds
	}
	isAllowedBucket := func(b string) bool {
		for _, a := range allowed {
			if b == a {
				return true
			}
		}
		return false
	}
	filtered := make([]explorationRound, 0, len(rounds))
	for _, round := range rounds {
		// Keep rounds whose title matches the allowed buckets, or
		// explicit request rounds (which may contain any file type).
		if round.Title == "explicit request targets" {
			filteredTargets := make([]string, 0, len(round.Targets))
			for _, target := range round.Targets {
				if isAllowedBucket(surveyPriorityBucketForPath(target)) {
					filteredTargets = append(filteredTargets, target)
				}
			}
			if len(filteredTargets) > 0 {
				filtered = append(filtered, explorationRound{
					Title:   round.Title,
					Targets: filteredTargets,
					Reason:  round.Reason,
				})
			}
			continue
		}
		// For bucket rounds, check if the title matches any allowed bucket.
		for _, a := range allowed {
			if strings.Contains(strings.ToLower(round.Title), a) ||
				(a == "docs" && strings.Contains(strings.ToLower(round.Title), "documentation")) ||
				(a == "source" && (strings.Contains(strings.ToLower(round.Title), "source") || strings.Contains(strings.ToLower(round.Title), "entrypoint") || strings.Contains(strings.ToLower(round.Title), "domain"))) ||
				(a == "config" && (strings.Contains(strings.ToLower(round.Title), "config") || strings.Contains(strings.ToLower(round.Title), "ci"))) {
				filtered = append(filtered, round)
				break
			}
		}
	}
	return filtered
}

func selectResearcherReadTarget(input string) (string, bool) {
	targets := selectResearcherReadTargets(input)
	if len(targets) == 0 {
		return "", false
	}
	return targets[0], true
}

type explorationDepth string

const (
	explorationDepthShallow explorationDepth = "shallow"
	explorationDepthDefault explorationDepth = "default"
	explorationDepthDeep    explorationDepth = "deep"
)

func selectResearcherReadTargets(input string) []string {
	depth := determineExplorationDepth(input)
	targets := []string{}
	seen := map[string]bool{}
	addTarget := func(path string) {
		cleaned := filepath.Clean(path)
		key := strings.ToLower(cleaned)
		if key == "." || seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, cleaned)
	}
	addExisting := func(candidate string) {
		if path, ok := existingResearcherReadTarget(candidate); ok {
			addTarget(path)
		}
	}

	for _, candidate := range requestFileTargetCandidates(input) {
		addExisting(candidate)
	}
	if len(targets) > 0 {
		return targets
	}

	// P2-3: When the task involves code implementation, discover and
	// include source files relevant to the task domain (interface
	// definitions, registries, type files). This gives the Builder's
	// LLM accurate interface signatures instead of guessing.
	if looksLikeCodeImplementationTask(input) {
		for _, candidate := range codeTaskSuggestedTargets(input, 5) {
			addExisting(candidate)
		}
	}

	for _, candidate := range repositorySurveyTargets(input, depth) {
		addExisting(candidate)
	}
	if len(targets) == 0 {
		for _, candidate := range discoverExistingSurveyTargets() {
			addExisting(candidate)
		}
	}
	return targets
}

func requestFileTargetCandidates(input string) []string {
	seen := map[string]bool{}
	candidates := []string{}
	reportOutputs := requestedReportOutputTargets(input)
	add := func(candidate string) {
		cleaned := cleanRequestFileTarget(candidate)
		if cleaned == "" || seen[cleaned] {
			return
		}
		if reportOutputs[strings.ToLower(filepath.Clean(cleaned))] {
			return
		}
		if isAnalysisReportFile(cleaned) && !looksLikeExplicitReportInspection(input) {
			return
		}
		seen[cleaned] = true
		candidates = append(candidates, cleaned)
	}

	for _, token := range strings.FieldsFunc(input, isRequestTargetSeparator) {
		add(token)
	}

	lowered := strings.ToLower(input)
	if strings.Contains(lowered, "cli docs") || strings.Contains(lowered, "cli doc") || strings.Contains(lowered, "cli guide") {
		add(projectfiles.CLIGuide)
	}
	if strings.Contains(lowered, "agent.yaml") || strings.Contains(lowered, "agent config") || strings.Contains(lowered, "agent configuration") {
		add("configs/agent.yaml")
	}

	return candidates
}

func cleanRequestFileTarget(candidate string) string {
	trimmed := strings.TrimSpace(candidate)
	trimmed = strings.Trim(trimmed, "`'\"[](){}<>.,;:，。；：、")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		return ""
	}
	if strings.HasPrefix(trimmed, "#") {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.ReplaceAll(trimmed, "\\", string(filepath.Separator))
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return ""
	}
	if !looksLikeProjectFileTarget(cleaned) {
		return ""
	}
	return cleaned
}

func reportOutputTargetAliases() []string {
	return projectfiles.ReportOutputAliases()
}

func requestedReportOutputTargets(input string) map[string]bool {
	lowered := strings.ToLower(input)
	if !looksLikeReportOutputRequest(lowered) {
		return map[string]bool{}
	}
	targets := map[string]bool{}
	for _, alias := range reportOutputTargetAliases() {
		if strings.Contains(lowered, alias) {
			targets[strings.ToLower(filepath.Clean(alias))] = true
		}
	}
	for _, candidate := range requestMarkdownPathCandidates(input) {
		targets[strings.ToLower(filepath.Clean(candidate))] = true
	}
	return targets
}

func looksLikeReportOutputRequest(loweredInput string) bool {
	for _, token := range []string{
		"write",
		"output",
		"save",
		"summarize in",
		"report to",
		"生成",
		"输出",
		"写入",
		"保存",
		"记录在",
	} {
		if strings.Contains(loweredInput, token) {
			return true
		}
	}
	return false
}

func looksLikeProjectFileTarget(path string) bool {
	base := filepath.Base(path)
	if strings.Contains(path, string(filepath.Separator)) {
		return true
	}
	return strings.Contains(base, ".")
}

// looksLikeCodeImplementationTask detects whether the user's request
// implies code-level implementation work (beyond analysis/report).
// Used by the Researcher to include relevant source files in the survey.
func looksLikeCodeImplementationTask(input string) bool {
	lowered := strings.ToLower(input)
	for _, kw := range codeImplementationKeywords {
		if strings.Contains(lowered, kw) {
			return true
		}
	}
	// Also check for file extension mentions which imply code creation.
	for _, ext := range []string{".go", ".py", ".js", ".ts", ".rs", ".java", ".cs"} {
		if strings.Contains(lowered, ext) {
			return true
		}
	}
	return false
}

// codeImplementationKeywords is the set of task keywords that suggest
// the user wants code-level work rather than pure analysis.
var codeImplementationKeywords = []string{
	"plugin", "handler", "model", "template", "output", "service",
	"registry", "interface", "implement", "add a new", "create a new",
	"add new", "create new",
	"插件", "处理器", "服务", "模板", "注册",
	"provider", "llm", "接口",
}

// codeTaskSourceDirs maps code-implementation keywords to the source
// directories or files most likely to contain the relevant interface
// or type definitions.
var codeTaskSourceDirs = map[string][]string{
	"plugin":   {"internal/plugin/interface.go", "internal/plugins/registry.go"},
	"plugins":  {"internal/plugins/registry.go", "internal/plugin/interface.go"},
	"handler":  {"internal/handler"},
	"model":    {"internal/model/types.go"},
	"template": {"internal/template"},
	"output":   {"internal/output"},
	"manual":   {"internal/manual"},
	"auto":     {"internal/auto"},
	"preset":   {"internal/preset"},
	"registry": {"internal/plugins/registry.go"},
	"interface": {
		"internal/plugin/interface.go",
		"internal/model/types.go",
	},
	"服务": {"internal/service"},
	"插件": {"internal/plugin/interface.go", "internal/plugins/registry.go"},
	"模板": {"internal/template"},
	"注册": {"internal/plugins/registry.go"},
	// P8-4: Provider/LLM interface — when generating provider code,
	// the Builder needs to see the interface definition (CompletionRequest
	// struct fields) and at least one reference implementation.
	"provider": {"internal/llm/interface.go", "internal/llm/providers/deepseek/deepseek.go"},
	"llm":      {"internal/llm/interface.go"},
	"接口":       {"internal/llm/interface.go"},
}

// codeTaskSuggestedTargets returns source file paths relevant to the
// task's code domain keywords. These are interface/type/registry files
// the Researcher should read so the Builder has accurate type signatures.
func codeTaskSuggestedTargets(input string, limit int) []string {
	lowered := strings.ToLower(input)
	candidates := []string{}
	seen := map[string]bool{}

	// P5-5: Scan for explicitly named file paths in the user's request.
	// When the user says "analyze natural_language.go and main_test.go",
	// those exact files should be the first targets, before any keyword
	// matching. Without this, the Researcher only reads README.md and
	// misses the files the user actually asked about.
	explicitFiles := extractExplicitFilePaths(input)
	for _, f := range explicitFiles {
		if seen[f] {
			continue
		}
		if _, statErr := os.Stat(f); statErr == nil {
			seen[f] = true
			candidates = append(candidates, f)
		}
	}

	// Collect target dirs/files for each matching keyword.
	for kw, dirs := range codeTaskSourceDirs {
		if strings.Contains(lowered, kw) {
			for _, dir := range dirs {
				if seen[dir] {
					continue
				}
				seen[dir] = true
				// Check if it's a directory (discover files inside) or a
				// specific file path.
				info, err := os.Stat(dir)
				if err != nil {
					continue
				}
				if info.IsDir() {
					// Discover up to 3 source files in this directory.
					files := sourceFilesInDir(dir, 3)
					candidates = append(candidates, files...)
				} else {
					candidates = append(candidates, dir)
				}
			}
		}
	}
	if len(candidates) > limit {
		return candidates[:limit]
	}
	return candidates
}

// extractExplicitFilePaths scans the user's input for explicitly named
// file paths — any token that looks like a path with a code or document
// extension (.go, .md, .yaml, .json, etc.). Returns verified paths that
// exist on disk. P5-5: enables Researcher to prioritize files the user
// actually named.
func extractExplicitFilePaths(input string) []string {
	// Match patterns like "natural_language.go", "cmd/avatars/main.go",
	// "process_record.md", "configs/agent.yaml"
	re := regexp.MustCompile(`[\w/\-\.]+\.(go|md|yaml|yml|json|txt|py|js|ts|jsx|tsx|sql|html|css|mod|sum)`)
	matches := re.FindAllString(input, -1)
	seen := map[string]bool{}
	var result []string
	for _, m := range matches {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		// Only include if it looks like a real path (contains / or a known dir)
		if _, err := os.Stat(m); err == nil {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

// sourceFilesInDir returns up to limit source files (non-test) in the
// given directory.
func sourceFilesInDir(dir string, limit int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := []string{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
		if len(files) >= limit {
			break
		}
	}
	return files
}

func isRequestTargetSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', ',', ';', ':', '"', '\'', '`', '<', '>', '[', ']', '(', ')', '{', '}', '，', '。', '；', '：', '、':
		return true
	default:
		return false
	}
}

func existingResearcherReadTarget(candidate string) (string, bool) {
	path := filepath.Clean(candidate)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// discoverExistingSurveyTargets probes disk for real project files before
// Researcher claims "nearly empty". Used when parallel survey nodes arrive
// without precomputed readTargets (Batch4/L).
func discoverExistingSurveyTargets() []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, candidate := range projectfiles.BootstrapSurveyTargets() {
		if path, ok := existingResearcherReadTarget(candidate); ok {
			add(path)
		}
	}
	for _, candidate := range []string{
		"go.mod", "package.json", "Cargo.toml", "pyproject.toml",
		"requirements.txt", "main.go", "README.md",
	} {
		if path, ok := existingResearcherReadTarget(candidate); ok {
			add(path)
		}
	}
	// Light walk for source entrypoints when a manifest exists.
	if len(out) > 0 || fileExists("go.mod") || fileExists("package.json") {
		_ = filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
			if err != nil || len(out) >= 8 {
				return filepath.SkipAll
			}
			if entry.IsDir() {
				base := filepath.Base(path)
				if path != "." && (base == ".git" || base == "node_modules" || base == "vendor" || base == ".avatars" || strings.HasPrefix(base, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			lower := strings.ToLower(path)
			if strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".py") ||
				strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".js") ||
				strings.HasSuffix(lower, ".rs") {
				if !strings.Contains(lower, "_test.") {
					add(path)
				}
			}
			return nil
		})
	}
	return out
}

func probeBootstrapFile() (string, bool) {
	for _, candidate := range bootstrapFileCandidates() {
		if path, ok := existingResearcherReadTarget(candidate); ok {
			return path, true
		}
	}
	return "", false
}

func bootstrapFileCandidates() []string {
	return projectfiles.BootstrapFileCandidates()
}

func repositorySurveyTargetCandidates(input string, depth explorationDepth) []string {
	candidates := bootstrapFileCandidates()
	if intentLooksLikeIssueHunt(input) || intentLooksLikeRepositoryAnalysis(input) {
		candidates = append(candidates, repositoryManifestCandidates()...)
		candidates = append(candidates, repositoryEntrypointCandidates()...)
		candidates = append(candidates, repositoryConfigCandidates()...)
		candidates = append(candidates, repositoryCICandidates()...)
		if !currentRepositoryLooksLikeAvatars() {
			sourceLimit, configLimit := explorationDiscoveryBudgets(depth)
			candidates = append(candidates, discoveredProjectSourceCandidates(sourceLimit)...)
			candidates = append(candidates, discoveredProjectConfigCandidates(configLimit)...)
		}
		if depth == explorationDepthDeep {
			candidates = append(candidates, discoveredProjectTestCandidates(explorationDiscoveryBudget(depth)/4)...)
		}
	}
	return candidates
}

func repositorySurveyTargets(input string, depth explorationDepth) []string {
	candidates := repositorySurveyTargetCandidates(input, depth)
	if intentLooksLikeIssueHunt(input) || intentLooksLikeRepositoryAnalysis(input) {
		candidates = append(candidates, discoverRepositorySurveyCandidates(explorationDiscoveryBudget(depth))...)
	}
	return rankRepositorySurveyTargetsForInput(input, candidates, explorationTargetBudget(depth))
}

func discoverRepositorySurveyCandidates(limit int) []string {
	entries := []surveyCandidate{}
	_ = filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(entries) >= limit*3 {
			return nil
		}
		if entry.IsDir() {
			if path != "." && isIgnoredSurveyPath(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredSurveyFile(path) || isTestSourceFile(path) {
			return nil
		}
		if !isSurveySourceFile(path) && !isSurveyConfigFile(path) && !isSurveyDocFile(path) && !isSurveyManifestFile(path) {
			return nil
		}
		cleaned := filepath.Clean(path)
		if isDefaultAvatarsSurveyPath(cleaned) && !currentRepositoryLooksLikeAvatars() {
			return nil
		}
		entries = append(entries, surveyCandidate{Path: cleaned, Score: surveyFileScore(cleaned), Bucket: surveyPriorityBucketForPath(cleaned)})
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Bucket != entries[j].Bucket {
			return surveyBucketOrder(entries[i].Bucket) < surveyBucketOrder(entries[j].Bucket)
		}
		if entries[i].Score == entries[j].Score {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Score > entries[j].Score
	})
	paths := []string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		key := strings.ToLower(entry.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		paths = append(paths, entry.Path)
		if len(paths) >= limit {
			break
		}
	}
	return paths
}

type surveyCandidate struct {
	Path   string
	Score  int
	Bucket string
}

func rankRepositorySurveyTargets(candidates []string, limit int) []string {
	return rankRepositorySurveyTargetsForInput("", candidates, limit)
}

func rankRepositorySurveyTargetsForInput(input string, candidates []string, limit int) []string {
	entries := []surveyCandidate{}
	seen := map[string]bool{}
	reportOutputs := requestedReportOutputTargets(input)
	for _, candidate := range candidates {
		cleaned, ok := existingResearcherReadTarget(candidate)
		if !ok {
			continue
		}
		key := strings.ToLower(cleaned)
		if cleaned == "." || seen[key] || isIgnoredSurveyFile(cleaned) || (isDefaultAvatarsSurveyPath(cleaned) && !currentRepositoryLooksLikeAvatars()) {
			continue
		}
		if reportOutputs[key] {
			continue
		}
		if (isAnalysisReportFile(cleaned) || isGeneratedAnalysisReportPath(cleaned)) && !looksLikeExplicitReportInspection(input) {
			continue
		}
		seen[key] = true
		entries = append(entries, surveyCandidate{Path: cleaned, Score: surveyFileScore(cleaned), Bucket: surveyPriorityBucketForPath(cleaned)})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Bucket != entries[j].Bucket {
			return surveyBucketOrder(entries[i].Bucket) < surveyBucketOrder(entries[j].Bucket)
		}
		if entries[i].Score == entries[j].Score {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Score > entries[j].Score
	})
	targets := []string{}
	bucketCounts := map[string]int{}
	bucketCaps := surveyBucketCapsForInput(input)
	cappedEntries := []surveyCandidate{}
	for _, entry := range entries {
		if cap := bucketCaps[entry.Bucket]; cap > 0 && bucketCounts[entry.Bucket] >= cap {
			cappedEntries = append(cappedEntries, entry)
			continue
		}
		targets = append(targets, entry.Path)
		bucketCounts[entry.Bucket]++
	}
	if limit <= 0 {
		return targets
	}
	for _, entry := range cappedEntries {
		if len(targets) >= limit {
			break
		}
		if entry.Bucket == "docs" {
			continue
		}
		targets = append(targets, entry.Path)
	}
	if len(targets) > limit {
		return targets[:limit]
	}
	return targets
}

func surveyBucketCapsForInput(input string) map[string]int {
	if determineExplorationDepth(input) != explorationDepthDeep {
		return nil
	}
	return map[string]int{
		"docs":         8,
		"manifests":    6,
		"entrypoints":  5,
		"domain-logic": 14,
		"tests":        5,
		"config-ci":    6,
		"supplemental": 3,
	}
}

func determineExplorationDepth(input string) explorationDepth {
	lowered := strings.ToLower(input)
	for _, token := range []string{
		"deep analysis",
		"deeply",
		"deep",
		"find issues",
		"concrete risk",
		"concrete risks",
		"evidence",
		"impact",
		"verification",
		"找问题",
		"具体风险",
		"证据",
		"影响",
		"验证办法",
		"深入",
		"详查",
		"report concrete issues",
		"report",
		"ana.md",
		"anal.md",
		"outcome.md",
		"analysis.md",
		"report.md",
		"analysis report",
	} {
		if strings.Contains(lowered, token) {
			return explorationDepthDeep
		}
	}
	return explorationDepthDefault
}

func explorationDiscoveryBudget(depth explorationDepth) int {
	switch depth {
	case explorationDepthDeep:
		return 72
	default:
		return 40
	}
}

func explorationTargetBudget(depth explorationDepth) int {
	switch depth {
	case explorationDepthDeep:
		return 40
	default:
		return 28
	}
}

func explorationDiscoveryBudgets(depth explorationDepth) (int, int) {
	switch depth {
	case explorationDepthDeep:
		return 40, 18
	default:
		return 24, 12
	}
}

func surveyBucketOrder(bucket string) int {
	for index, candidate := range surveyPriorityBuckets() {
		if candidate.Key == bucket {
			return index
		}
	}
	return len(surveyPriorityBuckets())
}

func surveyFileScore(path string) int {
	cleaned := filepath.Clean(path)
	base := strings.ToLower(filepath.Base(cleaned))
	ext := strings.ToLower(filepath.Ext(cleaned))
	score := 0
	switch base {
	case "readme.md", "readme":
		score += 100
	case "go.mod", "package.json", "pyproject.toml", "cargo.toml", "pom.xml":
		score += 95
	case "main.go", "app.go", "main.py", "app.py", "server.py", "index.js", "index.ts", "app.js", "app.ts", "server.js", "server.ts":
		score += 90
	case "makefile", "dockerfile":
		score += 55
	}
	switch ext {
	case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".kt":
		score += 50
	case ".json", ".yaml", ".yml", ".toml", ".sql":
		score += 25
	case ".md":
		score += 20
	}
	lowered := strings.ToLower(cleaned)
	for _, part := range strings.Split(lowered, string(filepath.Separator)) {
		switch part {
		case "cmd", "internal", "pkg", "src", "app", "server":
			score += 18
		case "manual", "auto", "output", "plugin", "plugins", "preset", "memory", "model", "query", "sql":
			score += 14
		case "config", "configs", ".github":
			score += 10
		case "frontend", "web", "ui":
			score += 8
		}
	}
	for _, token := range []string{"workbook", "import", "export", "sql", "task", "runtime", "engine", "handler", "service", "controller", "plugin", "session", "manual", "query", "sqlite", "runner", "registry", "preset", "memory", "model"} {
		if strings.Contains(lowered, token) {
			score += 12
		}
	}
	depth := strings.Count(cleaned, string(filepath.Separator))
	score -= depth * 2
	return score
}

func currentRepositoryLooksLikeAvatars() bool {
	module, ok := firstGoModuleName(projectfiles.GoMod)
	if !ok {
		module = ""
	}
	return projectfiles.RepositoryLooksLikeAvatars(module, func(path string) bool {
		_, ok := existingResearcherReadTarget(path)
		return ok
	})
}

func firstGoModuleName(path string) (string, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	module, _ := parseGoModFacts(string(content))
	module = strings.TrimSpace(module)
	return module, module != ""
}

func repositoryManifestCandidates() []string {
	return []string{
		"go.mod",
		"go.sum",
		"package.json",
		"package-lock.json",
		"pnpm-lock.yaml",
		"yarn.lock",
		"pyproject.toml",
		"requirements.txt",
		"Cargo.toml",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"Makefile",
		"Dockerfile",
	}
}

func repositoryEntrypointCandidates() []string {
	candidates := []string{
		"main.go",
		"app.go",
		"main.py",
		"app.py",
		"server.py",
		"index.js",
		"index.ts",
		"app.js",
		"app.ts",
		"server.js",
		"server.ts",
		filepath.Join("src", "main.go"),
		filepath.Join("src", "main.py"),
		filepath.Join("src", "index.js"),
		filepath.Join("src", "index.ts"),
		filepath.Join("src", "main.js"),
		filepath.Join("src", "main.ts"),
		filepath.Join("src", "app.js"),
		filepath.Join("src", "app.ts"),
	}
	if currentRepositoryLooksLikeAvatars() {
		candidates = append(candidates, projectfiles.AvatarsRepositorySignaturePaths()...)
	}
	candidates = append(candidates, topLevelSourceCandidates(8)...)
	return candidates
}

func discoveredProjectSourceCandidates(limit int) []string {
	roots := []string{"cmd", "internal", "pkg", "src", "app", "server", "frontend"}
	candidates := []string{}
	_ = filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(candidates) >= limit {
			return nil
		}
		if entry.IsDir() {
			if path != "." && isIgnoredSurveyPath(path) {
				return filepath.SkipDir
			}
			if path != "." && !isUnderSurveyRoot(path, roots) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredSurveyFile(path) || isTestSourceFile(path) || !isSurveySourceFile(path) {
			return nil
		}
		if isDefaultAvatarsSurveyPath(path) && !currentRepositoryLooksLikeAvatars() {
			return nil
		}
		candidates = append(candidates, filepath.Clean(path))
		return nil
	})
	return candidates
}

func discoveredProjectConfigCandidates(limit int) []string {
	roots := []string{"config", "configs", "sql", filepath.Join("configs", "sql"), ".github"}
	candidates := []string{}
	_ = filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(candidates) >= limit {
			return nil
		}
		if entry.IsDir() {
			if path != "." && isIgnoredSurveyPath(path) {
				return filepath.SkipDir
			}
			if path != "." && !isUnderSurveyRoot(path, roots) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredSurveyFile(path) || !isSurveyConfigFile(path) {
			return nil
		}
		if isDefaultAvatarsSurveyPath(path) && !currentRepositoryLooksLikeAvatars() {
			return nil
		}
		candidates = append(candidates, filepath.Clean(path))
		return nil
	})
	return candidates
}

func discoveredProjectTestCandidates(limit int) []string {
	roots := []string{"cmd", "internal", "pkg", "src", "app", "server", "frontend"}
	candidates := []string{}
	_ = filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(candidates) >= limit {
			return nil
		}
		if entry.IsDir() {
			if path != "." && isIgnoredSurveyPath(path) {
				return filepath.SkipDir
			}
			if path != "." && !isUnderSurveyRoot(path, roots) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSurveyTestFile(path) {
			return nil
		}
		if isDefaultAvatarsSurveyPath(path) && !currentRepositoryLooksLikeAvatars() {
			return nil
		}
		candidates = append(candidates, filepath.Clean(path))
		return nil
	})
	return candidates
}

func isUnderSurveyRoot(path string, roots []string) bool {
	cleaned := filepath.Clean(path)
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		if cleaned == cleanRoot || strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) || strings.HasPrefix(cleanRoot, cleaned+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isSurveySourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".kt", ".cs", ".rb", ".php", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func isSurveyTestFile(path string) bool {
	return isTestSourceFile(path)
}

func isSurveyConfigFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".yaml", ".yml", ".toml", ".sql", ".env", ".sample", ".example":
		return true
	default:
		base := strings.ToLower(filepath.Base(path))
		return base == "dockerfile" || base == "makefile"
	}
}

func isSurveyDocFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "readme" {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md":
		return !isIgnoredSurveyFile(path)
	default:
		return false
	}
}

func isSurveyManifestFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "requirements.txt", "cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts", "makefile", "dockerfile":
		return true
	default:
		return false
	}
}

func isDefaultAvatarsSurveyPath(path string) bool {
	return projectfiles.IsDefaultAvatarsSurveyPath(path)
}

func topLevelSourceCandidates(limit int) []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil
	}
	candidates := []string{}
	for _, entry := range entries {
		if len(candidates) >= limit {
			break
		}
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if isIgnoredSurveyFile(name) || isTestSourceFile(name) {
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".kt", ".cs", ".rb", ".php":
			candidates = append(candidates, name)
		}
	}
	return candidates
}

func repositoryConfigCandidates() []string {
	candidates := []string{
		"config.yaml",
		"config.yml",
		"config.json",
		"settings.json",
		".env.example",
		".env.sample",
	}
	for _, dir := range []string{"config", "configs"} {
		candidates = append(candidates,
			filepath.Join(dir, "config.yaml"),
			filepath.Join(dir, "config.yml"),
			filepath.Join(dir, "config.json"),
			filepath.Join(dir, "settings.json"),
		)
	}
	return candidates
}

func repositoryCICandidates() []string {
	return []string{
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join(".github", "workflows", "ci.yaml"),
		filepath.Join(".github", "workflows", "test.yml"),
		filepath.Join(".github", "workflows", "test.yaml"),
		filepath.Join(".gitlab-ci.yml"),
	}
}

func isIgnoredSurveyFile(path string) bool {
	if projectfiles.IsAvatarsContextFile(path) || projectfiles.IsAnalysisReportFile(path) {
		return true
	}
	return isIgnoredSurveyPath(path)
}

func isIgnoredSurveyPath(path string) bool {
	cleaned := filepath.Clean(path)
	parts := strings.Split(cleaned, string(filepath.Separator))
	for index, part := range parts {
		lowered := strings.ToLower(part)
		switch lowered {
		case "avatars":
			if index == 0 {
				return true
			}
		case ".avatars", ".git", "bin", "node_modules", "vendor", "dist", "build", "coverage", "skills":
			return true
		}
	}
	return false
}

func isTestSourceFile(path string) bool {
	lowered := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(lowered, "_test.go") ||
		strings.HasSuffix(lowered, ".test.ts") ||
		strings.HasSuffix(lowered, ".test.tsx") ||
		strings.HasSuffix(lowered, ".test.js") ||
		strings.HasSuffix(lowered, ".spec.ts") ||
		strings.HasSuffix(lowered, ".spec.tsx") ||
		strings.HasSuffix(lowered, ".spec.js")
}

func intentLooksLikeRepositoryAnalysis(input string) bool {
	lowered := strings.ToLower(input)
	for _, token := range []string{"analyze", "inspect", "review", "survey", "repository", "project", "repo", "分析", "回审", "查看项目", "该项目"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func intentLooksLikeIssueHunt(input string) bool {
	lowered := strings.ToLower(input)
	for _, token := range []string{"issue", "problem", "bug", "risk", "drift", "find problems", "找问题", "问题", "风险", "隐患"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}
