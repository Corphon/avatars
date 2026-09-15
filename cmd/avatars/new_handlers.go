package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"avatars/internal/tasks"
	"avatars/internal/tools"
)

// P4-4b: Analysis handlers for tasks that previously fell through to
// the pipeline's empty skill generation. These provide deterministic
// direct answers for common analysis requests.

// --- Match functions ---

func looksLikeDirectoryTreeQuestion(lowered string) bool {
	treeSignal := strings.Contains(lowered, "tree") ||
		strings.Contains(lowered, "树") ||
		strings.Contains(lowered, "tree状") ||
		strings.Contains(lowered, "结构图") ||
		strings.Contains(lowered, "dir") ||
		strings.Contains(lowered, "目录结构") ||
		strings.Contains(lowered, "文件夹结构")
	projectSignal := strings.Contains(lowered, "project") ||
		strings.Contains(lowered, "项目") ||
		strings.Contains(lowered, "repo")
	return treeSignal && projectSignal
}

func looksLikeTopImportsQuestion(lowered string) bool {
	importSignal := strings.Contains(lowered, "import") ||
		strings.Contains(lowered, "导入") ||
		strings.Contains(lowered, "依赖") ||
		strings.Contains(lowered, "包")
	topSignal := strings.Contains(lowered, "most") ||
		strings.Contains(lowered, "top") ||
		strings.Contains(lowered, "最多") ||
		strings.Contains(lowered, "前") ||
		strings.Contains(lowered, "常用")
	return importSignal && topSignal
}

func looksLikeCodeLineCountQuestion(lowered string) bool {
	countSignal := strings.Contains(lowered, "count") ||
		strings.Contains(lowered, "统计") ||
		strings.Contains(lowered, "how many") ||
		strings.Contains(lowered, "多少")
	lineSignal := strings.Contains(lowered, "line") ||
		strings.Contains(lowered, "行") ||
		strings.Contains(lowered, "loc")
	codeSignal := strings.Contains(lowered, "code") ||
		strings.Contains(lowered, "代码") ||
		strings.Contains(lowered, "go") ||
		strings.Contains(lowered, "source")
	return countSignal && lineSignal && codeSignal
}

// --- Answer functions ---

func generateDirectoryTree(root string) (string, error) {
	excludedDirs := map[string]bool{
		".git": true, ".avatars": true, "avatars": true,
		"node_modules": true, "vendor": true, "dist": true,
		"build": true, "__pycache__": true, ".github": true,
		"data": true, "temp": true, "logs": true,
	}

	type entry struct {
		path  string
		depth int
		isDir bool
	}
	var entries []entry
	maxDepth := 4

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if excludedDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
		}
		rel, _ := filepath.Rel(root, path)
		depth := len(strings.Split(filepath.ToSlash(rel), "/")) - 1
		if d.IsDir() {
			depth = len(strings.Split(filepath.ToSlash(rel), "/"))
		}
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entries = append(entries, entry{path: rel, depth: depth, isDir: d.IsDir()})
		if len(entries) > 200 {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Project directory tree (max depth 4):\n\n")
	sb.WriteString(filepath.Base(root) + "/\n")

	for i, e := range entries {
		prefix := strings.Repeat("  ", e.depth)
		if i < len(entries)-1 {
			nextDepth := entries[i+1].depth
			if nextDepth > e.depth && e.isDir {
				prefix += "├── "
			} else if nextDepth <= e.depth {
				prefix += "└── "
			} else {
				prefix += "├── "
			}
		} else {
			prefix += "└── "
		}
		name := filepath.Base(e.path)
		if e.isDir {
			name += "/"
		}
		sb.WriteString(prefix + name + "\n")
	}

	count, _ := countProjectFiles(root)
	sb.WriteString(fmt.Sprintf("\nTotal: %d files\n", count))
	return sb.String(), nil
}

func analyzeTopGoImports(root string) (string, error) {
	importCount := make(map[string]int)
	fileCount := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".avatars" || name == "avatars" ||
				name == "vendor" || name == "node_modules" || name == "build" ||
				name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fileCount++
		lines := strings.Split(string(content), "\n")
		inImport := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "import (" {
				inImport = true
				continue
			}
			if inImport {
				if trimmed == ")" {
					inImport = false
					continue
				}
				pkg := strings.Trim(trimmed, "\t \"")
				if pkg != "" && strings.Contains(pkg, "/") {
					parts := strings.Split(pkg, "/")
					if len(parts) >= 3 {
						pkg = strings.Join(parts[:3], "/")
					}
					importCount[pkg]++
				}
			}
			if strings.HasPrefix(trimmed, "import \"") {
				pkg := strings.TrimPrefix(trimmed, "import \"")
				pkg = strings.TrimSuffix(pkg, "\"")
				if strings.Contains(pkg, "/") {
					parts := strings.Split(pkg, "/")
					if len(parts) >= 3 {
						pkg = strings.Join(parts[:3], "/")
					}
					importCount[pkg]++
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	type pkgFreq struct {
		pkg   string
		count int
	}
	var sorted []pkgFreq
	for pkg, count := range importCount {
		sorted = append(sorted, pkgFreq{pkg, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Top imported packages (scanned %d .go files):\n\n", fileCount))
	limit := 10
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for i := 0; i < limit; i++ {
		sb.WriteString(fmt.Sprintf("%d. %s — %d imports\n", i+1, sorted[i].pkg, sorted[i].count))
	}
	if len(sorted) == 0 {
		sb.WriteString("No Go files with imports found.\n")
	}
	return sb.String(), nil
}

func countCodeLines(root string) (string, error) {
	totalLines := 0
	totalFiles := 0
	byExt := make(map[string]struct {
		files int
		lines int
	})

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".avatars" || name == "avatars" ||
				name == "vendor" || name == "node_modules" || name == "build" ||
				name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		codeExts := map[string]bool{
			".go": true, ".py": true, ".js": true, ".ts": true,
			".jsx": true, ".tsx": true, ".rs": true, ".java": true,
			".c": true, ".h": true, ".cpp": true, ".css": true,
		}
		if !codeExts[ext] {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lineCount := len(strings.Split(string(content), "\n"))
		totalLines += lineCount
		totalFiles++
		stats := byExt[ext]
		stats.files++
		stats.lines += lineCount
		byExt[ext] = stats
		return nil
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Code line count:\n\n"))
	sb.WriteString(fmt.Sprintf("Total: %d lines across %d files\n\n", totalLines, totalFiles))
	sb.WriteString("By extension:\n")
	exts := make([]string, 0, len(byExt))
	for ext := range byExt {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		s := byExt[ext]
		sb.WriteString(fmt.Sprintf("  %s: %d files, %d lines\n", ext, s.files, s.lines))
	}
	return sb.String(), nil
}

// --- P5-2: Cross-task context recall ---

func looksLikeLastAnalysisFindingsQuestion(lowered string) bool {
	// Must reference previous analysis
	prevSignal := strings.Contains(lowered, "上次") ||
		strings.Contains(lowered, "之前") ||
		strings.Contains(lowered, "前面") ||
		strings.Contains(lowered, "刚") ||
		strings.Contains(lowered, "last") ||
		strings.Contains(lowered, "previous") ||
		strings.Contains(lowered, "earlier") ||
		strings.Contains(lowered, "刚才")
	if !prevSignal {
		return false
	}
	// Must ask about findings/results/issues
	findingSignal := strings.Contains(lowered, "发现") ||
		strings.Contains(lowered, "问题") ||
		strings.Contains(lowered, "结果") ||
		strings.Contains(lowered, "分析") ||
		strings.Contains(lowered, "找到") ||
		strings.Contains(lowered, "findings") ||
		strings.Contains(lowered, "issues") ||
		strings.Contains(lowered, "result") ||
		strings.Contains(lowered, "总结") ||
		strings.Contains(lowered, "结论")
	return findingSignal
}

func summarizeLastAnalysisFindings(root string) (string, error) {
	manager := tasks.NewManager(filepath.Join(root, ".avatars", "tasks"))
	workspaces, err := manager.List()
	if err != nil {
		return "No previous analysis found — no task workspaces exist yet.", nil
	}

	// Find the workspace with the latest run
	var latest *tasks.Workspace
	var latestTime time.Time
	for _, ws := range workspaces {
		if ws.RunCount == 0 {
			continue
		}
		// Use the workspace itself — LatestSummary carries the synthesis
		if ws.LatestSummary != "" {
			info, statErr := os.Stat(filepath.Join(ws.RootDir))
			if statErr == nil && info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				cp := ws
				latest = &cp
			}
		}
	}

	if latest == nil {
		return "No previous analysis results found — no completed task with synthesis output.", nil
	}

	var sb strings.Builder
	sb.WriteString("Previous analysis findings:\n\n")
	sb.WriteString(fmt.Sprintf("Task: %s\n", latest.ID))
	if strings.TrimSpace(latest.Title) != "" {
		sb.WriteString(fmt.Sprintf("Request: %s\n", latest.Title))
	}
	if strings.TrimSpace(latest.LatestSummary) != "" {
		summary := latest.LatestSummary
		// Truncate very long summaries
		if len(summary) > 2000 {
			summary = summary[:2000] + "..."
		}
		sb.WriteString(fmt.Sprintf("\nSynthesis: %s\n", summary))
	}
	if transcript := latest.LatestTranscriptPath(); transcript != "" {
		sb.WriteString(fmt.Sprintf("\nFull transcript: %s\n", filepath.ToSlash(transcript)))
	}
	sb.WriteString(fmt.Sprintf("\nRun count: %d\n", latest.RunCount))
	return sb.String(), nil
}

// --- Emergence: cross-reference analysis ---

func looksLikeMissingTestsQuestion(lowered string) bool {
	// P5-4: Must specifically ask about missing test FILES, not test coverage.
	// "测试覆盖" / "test coverage" → NOT a missing-test-files question.
	if strings.Contains(lowered, "覆盖") || strings.Contains(lowered, "coverage") ||
		strings.Contains(lowered, "覆盖率") {
		return false
	}
	testSignal := strings.Contains(lowered, "test") ||
		strings.Contains(lowered, "测试") ||
		strings.Contains(lowered, "_test")
	missingSignal := strings.Contains(lowered, "missing") ||
		strings.Contains(lowered, "没有") ||
		strings.Contains(lowered, "缺少") ||
		strings.Contains(lowered, "without") ||
		strings.Contains(lowered, "no test") ||
		strings.Contains(lowered, "not have test")
	// Require explicit source-file context: asking about .go files that
	// lack _test.go counterparts, not general test coverage questions.
	fileSignal := strings.Contains(lowered, "_test.go") ||
		strings.Contains(lowered, "源文件") ||
		strings.Contains(lowered, "go文件") ||
		strings.Contains(lowered, "go file") ||
		strings.Contains(lowered, "对应的测试") ||
		strings.Contains(lowered, "缺少测试") ||
		strings.Contains(lowered, "没有测试") ||
		strings.Contains(lowered, "without test") ||
		strings.Contains(lowered, "missing test")
	return testSignal && missingSignal && fileSignal
}

func findMissingTests(root string) (string, error) {
	excludedDirs := map[string]bool{
		".git": true, ".avatars": true, "avatars": true,
		"vendor": true, "node_modules": true, "build": true, "dist": true,
	}

	// Collect all .go files
	goFiles := make(map[string]bool) // dir -> filenames
	var testFiles []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if excludedDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasSuffix(d.Name(), "_test.go") {
			testFiles = append(testFiles, rel)
		} else {
			goFiles[rel] = true
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Build set of expected test paths
	hasTest := make(map[string]bool)
	for _, tf := range testFiles {
		base := strings.TrimSuffix(tf, "_test.go") + ".go"
		hasTest[base] = true
	}

	var missing []string
	for gf := range goFiles {
		if !hasTest[gf] && !strings.HasSuffix(gf, "_test.go") {
			missing = append(missing, gf)
		}
	}
	sort.Strings(missing)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Go files without corresponding _test.go (%d of %d):\n\n", len(missing), len(goFiles)))
	if len(missing) == 0 {
		sb.WriteString("All Go files have corresponding test files. ✅\n")
	} else {
		for _, m := range missing {
			sb.WriteString(fmt.Sprintf("  - %s\n", filepath.ToSlash(m)))
		}
	}
	return sb.String(), nil
}

// --- P6: Intelligent shell analysis (root fix, v2) ---
// Package-level var passes user input from Match to Answer.
// This enables the handler to dynamically build shell commands
// tailored to what the user actually asked, rather than running
// fixed commands or requiring one handler per analysis type.
var lastShellAnalysisInput string

func looksLikeShellAnalysisQuestion(lowered string) bool {
	// P6: Remember the input so Answer can build targeted commands.
	// This handler is priority 25 (lowest) — it only fires when no
	// higher-priority specific handler matched. So by the time we
	// get here, we know the request needs generic shell analysis.
	//
	// S6.3: Never steal work that should become safe_run (write report /
	// implement / edit). Those belong to the task command builder or LLM.
	if looksLikeShellAnalysisWorkRequest(lowered) {
		return false
	}
	patterns := []string{
		// S6.8: Do NOT include bare 分析/analyze — those are work/clarify
		// intents and must not steal into local shell (root cause of
		// "分析一下 auth" → find catchall). Keep shell-shaped verbs only.
		"找出", "搜索", "统计", "查找", "列出",
		"find", "search", "count", "list",
		"多少", "how many", "tree", "树", "结构",
		"导入", "import", "依赖", "函数", "func",
		"todo", "fixme", "行数", "line count",
	}
	for _, p := range patterns {
		if strings.Contains(lowered, p) {
			lastShellAnalysisInput = lowered
			return true
		}
	}
	return false
}

// looksLikeShellAnalysisWorkRequest detects analysis phrasing that still
// implies a task (write report / mutate), not a local shell Q&A. S6.3.
func looksLikeShellAnalysisWorkRequest(lowered string) bool {
	workSignals := []string{
		"写入", "写到", "输出到", "总结写入", "write to", "write into",
		"outcome.md", "report.md", "anal.md",
		"修复", "实现", "改一下", "修改", "fix ", "implement", "edit ",
	}
	for _, s := range workSignals {
		if strings.Contains(lowered, s) {
			return true
		}
	}
	return false
}

// looksLikeReadOnlyModuleAnalysisRequest detects scoped read-only analysis
// (e.g. "分析一下 auth 模块") that must become plan-mode safe_run when the
// LLM is unavailable — without re-enabling the shell-analysis catchall.
func looksLikeReadOnlyModuleAnalysisRequest(lowered string) bool {
	if looksLikeShellAnalysisWorkRequest(lowered) {
		return false
	}
	if strings.Contains(lowered, "不要改") || strings.Contains(lowered, "只读") || strings.Contains(lowered, "read-only") {
		return strings.Contains(lowered, "分析") || strings.Contains(lowered, "analyze") || strings.Contains(lowered, "review")
	}
	moduleScoped := strings.Contains(lowered, "模块") || strings.Contains(lowered, " module") || strings.HasSuffix(lowered, "module")
	if moduleScoped && (strings.Contains(lowered, "分析一下") || strings.Contains(lowered, "分析下") ||
		strings.Contains(lowered, "analyze the") || strings.Contains(lowered, "review the")) {
		return true
	}
	return false
}

// looksLikeOfflineWorkRequest gates the offline task-command builder so
// out-of-scope chat does not become safe_run when the LLM is unavailable. S6.3.
func looksLikeOfflineWorkRequest(lowered string) bool {
	if looksLikeShellAnalysisWorkRequest(lowered) {
		return true
	}
	if looksLikeImplementationWorkIntent(lowered) {
		return true
	}
	if looksLikeSimpleScriptRequest(lowered) {
		return true
	}
	if looksLikeBootstrapProjectRequest(lowered) || looksLikeBootstrapLanguageFollowUp(lowered) {
		return true
	}
	if _, ok := extractExplicitPatchSpec(lowered); ok {
		return true
	}
	// Analysis / inspect without write still becomes plan-mode run when phrased
	// as a project task (not weather/smalltalk).
	if looksLikeReadOnlySurveyIntent(lowered) {
		return true
	}
	if looksLikeReadOnlyModuleAnalysisRequest(lowered) {
		return true
	}
	if strings.Contains(lowered, "survey depth") || strings.Contains(lowered, "runtime readiness") {
		return true
	}
	analysisSignals := []string{"分析项目", "analyze this", "analyze the project", "找问题", "inspect the", "inspect ", "review the code"}
	for _, s := range analysisSignals {
		if strings.Contains(lowered, s) {
			return true
		}
	}
	// Greenfield / multi-phase build phrasing (NL smoke: REST API + PostgreSQL).
	greenfieldSignals := []string{
		"从零", "从零搭", "从零开始", "分阶段", "分阶段交付", "先出计划", "先出计划和",
		"multi-phase", "greenfield", "from scratch",
	}
	for _, s := range greenfieldSignals {
		if strings.Contains(lowered, s) {
			return true
		}
	}
	return false
}

func executeShellAnalysis(root string) (string, error) {
	_ = root
	input := lastShellAnalysisInput
	commands := tools.BuildAnalysisCommands(input, 5)
	return tools.ExecuteAnalysisCommands(commands, 2000, 15*time.Second), nil
}

// --- P5-6: Large file tail-read ---

func looksLikeLargeFileTailQuestion(lowered string) bool {
	// Must mention reading tail/last N lines
	tailSignal := strings.Contains(lowered, "最后") ||
		strings.Contains(lowered, "末尾") ||
		strings.Contains(lowered, "结尾") ||
		strings.Contains(lowered, "last") ||
		strings.Contains(lowered, "tail") ||
		strings.Contains(lowered, "尾部")
	if !tailSignal {
		return false
	}
	// Must mention lines count
	lineSignal := strings.Contains(lowered, "行") ||
		strings.Contains(lowered, "line") ||
		strings.Contains(lowered, "lines")
	if !lineSignal {
		return false
	}
	// Must contain a file extension (any text/code file) — generic, no hardcoding
	hasFileExt := strings.Contains(lowered, ".md") ||
		strings.Contains(lowered, ".txt") ||
		strings.Contains(lowered, ".log") ||
		strings.Contains(lowered, ".yaml") ||
		strings.Contains(lowered, ".json") ||
		strings.Contains(lowered, ".go") ||
		strings.Contains(lowered, ".py") ||
		strings.Contains(lowered, ".js") ||
		strings.Contains(lowered, ".csv")
	return hasFileExt
}

// extractFilePathFromNL scans input for a plausible file path (contains
// a known extension and optionally a directory separator). Returns the
// first match, or empty string. Generic — works with any filename.
func extractFilePathFromNL(input string) string {
	re := regexp.MustCompile(`[\w/\-\\]+\.(md|txt|log|yaml|yml|json|go|py|js|csv)`)
	matches := re.FindAllString(input, -1)
	for _, m := range matches {
		m = strings.TrimSpace(m)
		// Prefer paths that look like real files (contain / or \)
		if strings.Contains(m, "/") || strings.Contains(m, "\\") {
			return m
		}
	}
	// Fallback: first match of any file-like token
	if len(matches) > 0 {
		return strings.TrimSpace(matches[0])
	}
	return ""
}

func readLargeFileTail(root string) (string, error) {
	// Find the largest text-ish file in the project. This is a
	// reasonable heuristic for "read the tail of the big file" — the
	// biggest text file in a project is usually process_record.md,
	// coding_plan.md, or a log file. No hardcoded filenames.
	excludedDirs := map[string]bool{
		".git": true, ".avatars": true, "avatars": true,
		"node_modules": true, "vendor": true, "dist": true,
		"build": true, "__pycache__": true,
	}
	textExts := map[string]bool{
		".md": true, ".txt": true, ".log": true, ".yaml": true, ".yml": true,
		".json": true, ".go": true, ".py": true, ".js": true, ".ts": true,
		".jsx": true, ".tsx": true, ".csv": true, ".html": true, ".css": true,
	}

	type candidate struct {
		path string
		size int64
	}
	var largest candidate

	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !textExts[ext] {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.Size() > largest.size {
			largest = candidate{path: path, size: info.Size()}
		}
		return nil
	})

	targetPath := largest.path
	if targetPath == "" {
		return "No text file found in project.", nil
	}

	f, err := os.Open(targetPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read all lines and take the last N
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	tailCount := 500
	if len(lines) < tailCount {
		tailCount = len(lines)
	}
	tailLines := lines[len(lines)-tailCount:]

	relPath, _ := filepath.Rel(root, targetPath)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Last %d lines of %s (%d total lines):\n\n", tailCount, relPath, len(lines)))
	for _, line := range tailLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Extract OPEN/TODO items
	sb.WriteString("\n---\nUnresolved items:\n")
	foundAny := false
	for _, line := range tailLines {
		lowered := strings.ToLower(line)
		if strings.Contains(lowered, "open") || strings.Contains(lowered, "未解决") ||
			strings.Contains(lowered, "⚠️") || strings.Contains(lowered, "todo") {
			if strings.Contains(lowered, "fixed") || strings.Contains(lowered, "✅") {
				continue
			}
			sb.WriteString(fmt.Sprintf("  %s\n", strings.TrimSpace(line)))
			foundAny = true
		}
	}
	if !foundAny {
		sb.WriteString("  (no unresolved items marked as OPEN/⚠️ in the tail)\n")
	}

	return sb.String(), nil
}

func extractLineCount(input string) int {
	re := regexp.MustCompile(`(\d+)\s*行|last\s*(\d+)\s*line|(\d+)\s*lines`)
	matches := re.FindStringSubmatch(strings.ToLower(input))
	if len(matches) > 1 {
		for _, m := range matches[1:] {
			if m != "" {
				if n, err := strconv.Atoi(m); err == nil {
					return n
				}
			}
		}
	}
	return 500
}

// --- P9-1: SQLite database query handler ---

func looksLikeSQLQueryQuestion(lowered string) bool {
	// S6 NL smoke (new_project_for_test): "PostgreSQL" in a build request must
	// not match bare "sql" and steal into sqlite-db-query.
	if looksLikeOfflineWorkRequest(lowered) {
		return false
	}
	if strings.Contains(lowered, "postgresql") || strings.Contains(lowered, "mysql") ||
		strings.Contains(lowered, "sqlite") || strings.Contains(lowered, "mongodb") {
		return false
	}
	return strings.Contains(lowered, "查询") ||
		strings.Contains(lowered, "query") ||
		strings.Contains(lowered, "数据库") ||
		strings.Contains(lowered, "database") ||
		strings.Contains(lowered, "select") ||
		strings.Contains(lowered, "多少条") ||
		strings.Contains(lowered, "记录") ||
		strings.Contains(lowered, "warm_lesson") ||
		strings.Contains(lowered, "task") && strings.Contains(lowered, "memory")
}

func queryProjectDatabase(root string) (string, error) {
	dbPath := filepath.Join(root, ".avatars", "memory", "hot-memory.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Try project-level memory
		dbPath = filepath.Join(root, ".avatars", "tasks")
		entries, _ := os.ReadDir(dbPath)
		for _, e := range entries {
			if e.IsDir() {
				candidate := filepath.Join(dbPath, e.Name(), "memory", "hot-memory.db")
				if _, err := os.Stat(candidate); err == nil {
					dbPath = candidate
					break
				}
			}
		}
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return "No SQLite database found in .avatars/ directory. Run a task first to create task memory.", nil
		}
	}

	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return "", fmt.Errorf("cannot open database: %w", err)
	}
	defer db.Close()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Database: %s\n\n", filepath.ToSlash(dbPath)))

	// List tables and row counts
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}

	if len(tables) == 0 {
		sb.WriteString("No tables found in database.\n")
		return sb.String(), nil
	}

	sb.WriteString(fmt.Sprintf("Tables (%d):\n", len(tables)))
	totalRows := 0
	for _, t := range tables {
		var count int
		if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM [%s]", t)).Scan(&count); err == nil {
			sb.WriteString(fmt.Sprintf("  %-40s %d rows\n", t, count))
			totalRows += count
		}
	}
	sb.WriteString(fmt.Sprintf("\nTotal: %d rows across %d tables\n", totalRows, len(tables)))
	return sb.String(), nil
}
