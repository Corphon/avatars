package planner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// filePathRx matches Go file paths like "internal/rates/rates.go" or "main.go"
// in task descriptions.
var filePathRx = regexp.MustCompile(`[\w./-]+\.(go|py|js|mjs|ts|tsx|rs|java|rb)`)

// pkgDirRx matches package directory references in task descriptions like
// "internal/models 数据模型", "internal/storage JSON持久化", "internal/handlers HTTP处理器".
// These are directory paths without file extensions — common in Planner task decomposition.
var pkgDirRx = regexp.MustCompile(`(?:^|[,\s])((?:internal|pkg|cmd|lib|src)/[\w/]+)(?:\s|$|，|,)`)

// extractFileTargets parses a task description and returns a deduplicated list
// of file paths that the task explicitly mentions as creation/modification targets.
// Also detects package directory references (e.g., "internal/models") and infers
// default filenames for them (e.g., "internal/models/models.go" for Go projects).
func extractFileTargets(task string) []string {
	seen := make(map[string]bool)
	var files []string

	// 1. Explicit file paths with extensions.
	for _, match := range filePathRx.FindAllString(task, -1) {
		clean := strings.Trim(match, `"'(),;:`)
		key := strings.ToLower(clean)
		if seen[key] || clean == "" {
			continue
		}
		seen[key] = true
		files = append(files, clean)
	}

	// 2. Package directory references (no file extension).
	// These appear in Planner-decomposed tasks like "internal/models 数据模型".
	for _, match := range pkgDirRx.FindAllStringSubmatch(task, -1) {
		if len(match) < 2 {
			continue
		}
		dir := strings.TrimSpace(match[1])
		key := strings.ToLower(dir)
		if seen[key] || dir == "" {
			continue
		}
		// Skip if this directory already has a file in the list.
		hasFile := false
		for _, f := range files {
			if strings.HasPrefix(strings.ToLower(f), key+"/") || strings.HasPrefix(strings.ToLower(f), key+".") {
				hasFile = true
				break
			}
		}
		if hasFile {
			continue
		}
		seen[key] = true
		// Infer filename: use the last path component as the base name.
		// "internal/models" → "internal/models/models.go" (if Go context detected)
		// We also add the directory itself as a target for the Builder.
		files = append(files, dir+"/")
	}

	return files
}

// sanitizeSplitTargets drops junk/debug paths and rewrites bare doc.go toward
// an on-disk library package when present (F42). Keeps Critic/Planner from
// spawning parallel Builder nodes that invent conflicting trees.
func sanitizeSplitTargets(files []string) []string {
	lib := ""
	if entries, err := os.ReadDir("."); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			name := ent.Name()
			switch name {
			case "cmd", "internal", "docs", "tmp", "vendor", "testdata":
				continue
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			// Prefer existing top-level dir that already has .go sources.
			if hasGoInDir(name) {
				lib = name
				break
			}
		}
	}
	if lib == "" {
		if data, err := os.ReadFile("go.mod"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
					mod = strings.Trim(mod, `"'`)
					lib = filepath.Base(filepath.FromSlash(mod))
					break
				}
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		clean := filepath.ToSlash(strings.TrimSpace(f))
		lower := strings.ToLower(clean)
		if clean == "" {
			continue
		}
		if strings.Contains(lower, "internal/debug") || strings.Contains(lower, "/tmp/") ||
			strings.HasPrefix(lower, "tmp/") || strings.Contains(lower, "internal/doc") ||
			strings.Contains(lower, "/dbg/") || strings.HasPrefix(lower, "_") {
			continue
		}
		base := filepath.Base(clean)
		if (clean == base || !strings.Contains(clean, "/")) && strings.EqualFold(base, "doc.go") && lib != "" {
			clean = lib + "/doc.go"
		}
		if lib != "" && strings.HasPrefix(lower, "internal/"+strings.ToLower(lib)) {
			rest := strings.TrimPrefix(clean, "internal/"+lib)
			rest = strings.TrimPrefix(rest, "internal/"+strings.ToLower(lib))
			rest = strings.TrimPrefix(rest, "/")
			if rest == "" || rest == "/" {
				clean = lib + "/"
			} else {
				clean = lib + "/" + strings.TrimPrefix(rest, "/")
			}
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func hasGoInDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(ent.Name()), ".go") {
			return true
		}
	}
	return false
}

// isNearlyEmptyProject reports whether the working tree lacks a meaningful
// source tree yet (greenfield / Phase-1 scaffold). Used to skip parallel
// per-file Builder splits that otherwise invent incompatible stacks.
func isNearlyEmptyProject(root string) bool {
	return countProjectSourceFiles(root) < 3
}

// isCompactSourceTree is true when the tree already has a small amount of
// source (library-sized). Three parallel surveys add LLM calls and bloat the
// Builder user turn without helping prefix cache.
func isCompactSourceTree(root string) bool {
	n := countProjectSourceFiles(root)
	return n >= 1 && n < 16
}

func countProjectSourceFiles(root string) int {
	if root == "" {
		root = "."
	}
	n := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			switch name {
			case ".git", ".avatars", "node_modules", "venv", ".venv", "__pycache__", "vendor", "docs":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
			".rs", ".java", ".kt", ".cs":
			n++
		}
		return nil
	})
	return n
}

// splitBuilderNodes replaces a single "node-build" with N focused Builder nodes,
// one per file. S4.4: when files have no cross-deps, siblings share the same
// DependsOn frontier (parallel). Serial chaining remains only when
// serial=true is requested by the caller.
// Critic/Synthesizer nodes are re-pointed to depend on ALL Builder nodes
// (or the last one when serial).
//
// P12: Per-file Builder decomposition prevents LLM output truncation.
func splitBuilderNodes(nodes []WorkflowNode, files []string) []WorkflowNode {
	return splitBuilderNodesMode(nodes, files, false)
}

// splitBuilderNodesSerial keeps the legacy serial chain (tests / forced order).
func splitBuilderNodesSerial(nodes []WorkflowNode, files []string) []WorkflowNode {
	return splitBuilderNodesMode(nodes, files, true)
}

func splitBuilderNodesMode(nodes []WorkflowNode, files []string, serial bool) []WorkflowNode {
	if len(files) <= 1 {
		return nodes // nothing to split
	}

	var result []WorkflowNode
	var builderIDs []string
	prevNodeID := ""

	for _, node := range nodes {
		if node.AssignedRole != "Builder" {
			// Re-point dependencies: if a node depended on "node-build",
			// it should now depend on all builder nodes (parallel join) or last (serial).
			var newDeps []string
			for _, dep := range node.DependsOn {
				if dep == "node-build" && len(builderIDs) > 0 {
					if serial {
						newDeps = append(newDeps, builderIDs[len(builderIDs)-1])
					} else {
						newDeps = append(newDeps, builderIDs...)
					}
				} else {
					newDeps = append(newDeps, dep)
				}
			}
			node.DependsOn = newDeps
			result = append(result, node)
			continue
		}

		firstDeps := append([]string(nil), node.DependsOn...)
		for i, file := range files {
			splitID := node.ID
			if len(files) > 1 {
				splitID = node.ID + "-" + padInt(i+1)
			}
			splitTitle := "Generate " + file + " AND its test file"
			if i == len(files)-1 && len(files) > 1 {
				splitTitle += " (final file)"
			}

			splitDeps := firstDeps
			if serial && i > 0 && prevNodeID != "" {
				splitDeps = []string{prevNodeID}
			}

			result = append(result, WorkflowNode{
				ID:           splitID,
				Title:        splitTitle,
				AssignedRole: "Builder",
				DependsOn:    splitDeps,
			})
			prevNodeID = splitID
			builderIDs = append(builderIDs, splitID)
		}
	}

	return result
}

// buildFocusedInput extracts the portion of the task relevant to a specific file.
// Used to give each split Builder node a focused prompt.
func buildFocusedInput(task string, file string) string {
	// Simple heuristic: if the task explicitly mentions the file, use the
	// original task with a file-scope prefix.
	return "Focus on " + file + ": " + task
}

func padInt(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// inferBuildPhases derives per-package build targets from a complex task
// description when no explicit file paths are found. Used as a fallback
// for medium+ complexity tasks to ensure per-file Builder splitting.
//
// Returns directory-based targets that the Builder can resolve to specific
// files. Order follows standard project dependency: models → storage →
// handlers → middleware → config → main wiring.
func inferBuildPhases(task string) []string {
	lowered := strings.ToLower(task)

	// Standard package directory patterns to look for.
	type phasePattern struct {
		dir     string
		keyword string // Chinese or English keyword in task
	}
	patterns := []phasePattern{
		{"internal/models", "models"},
		{"internal/models", "model"},
		{"internal/models", "数据模型"},
		{"internal/storage", "storage"},
		{"internal/storage", "store"},
		{"internal/storage", "持久化"},
		{"internal/storage", "json"},
		{"internal/handlers", "handlers"},
		{"internal/handlers", "handler"},
		{"internal/handlers", "http"},
		{"internal/handlers", "处理器"},
		{"internal/middleware", "middleware"},
		{"internal/middleware", "中间件"},
		{"internal/middleware", "日志"},
		{"internal/config", "config"},
		{"internal/config", "配置"},
		{"cmd/", "入口"},
		{"cmd/", "main"},
		{"cmd/", "entrypoint"},
	}

	seen := make(map[string]bool)
	var phases []string
	for _, p := range patterns {
		if strings.Contains(lowered, p.keyword) && !seen[p.dir] {
			seen[p.dir] = true
			phases = append(phases, p.dir+"/")
		}
	}

	// If nothing matched, return empty — do not invent internal/models+/cmd/
	// (F44: that bias buried public libraries under internal/).
	if len(phases) == 0 {
		return nil
	}

	// Always ensure cmd/ or main.go is last (wiring depends on everything else).
	var reordered []string
	var cmdTarget string
	for _, p := range phases {
		if strings.HasPrefix(p, "cmd/") || strings.Contains(p, "main") {
			cmdTarget = p
		} else {
			reordered = append(reordered, p)
		}
	}
	if cmdTarget != "" {
		reordered = append(reordered, cmdTarget)
	}

	return reordered
}

// countBuilderNodes returns the number of Builder-assigned nodes in the plan.
func countBuilderNodes(nodes []WorkflowNode) int {
	count := 0
	for _, n := range nodes {
		if n.AssignedRole == "Builder" {
			count++
		}
	}
	return count
}

// syncPlanPhaseCount updates the Phase Count field in docs/workflow/avatars_plan.md
// to match the actual number of Builder nodes. Called after splitBuilderNodes to
// keep the workflow plan in sync with the DAG node count.
func syncPlanPhaseCount(builderCount int) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	planPath := filepath.Join(cwd, "docs", "workflow", "avatars_plan.md")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return
	}
	content := string(data)
	newStr := fmt.Sprintf("**Phase Count**: %d", builderCount)
	// Handle ALL possible formats of Phase Count in the plan file:
	// "**Phase Count**: 1" (PreloadDocs format)
	// "Phase Count: 1" (legacy format)
	updated := false
	for _, oldFmt := range []string{
		"**Phase Count**: 1", "**Phase Count**: 2", "**Phase Count**: 3", "**Phase Count**: 4", "**Phase Count**: 5",
		"Phase Count: 1", "Phase Count: 2", "Phase Count: 3", "Phase Count: 4", "Phase Count: 5",
	} {
		if strings.Contains(content, oldFmt) {
			content = strings.Replace(content, oldFmt, newStr, 1)
			updated = true
			break
		}
	}
	if !updated {
		return // No Phase Count field found — plan file may not exist yet
	}
	os.WriteFile(planPath, []byte(content), 0644)
}
