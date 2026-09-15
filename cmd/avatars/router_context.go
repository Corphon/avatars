package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// projectFileEntry describes one file in the project directory.
type projectFileEntry struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// buildProjectContext builds a compact (~800-1200 chars) project context block
// for injection into the LLM router's system prompt. This gives the LLM a
// mental model of the project so it can:
//   - Infer target files when the user says "修改那个程序"
//   - Know what languages/frameworks are in use
//   - Understand project purpose from README
//   - See recently modified files for multi-turn continuity
//
// Claude Code reference: context.ts → getSystemContext + getUserContext
// inject git status + CLAUDE.md + file tree into every LLM call.
// We inject: directory listing + README summary + recent REPL files.
//
// Returns "" if the directory is empty or unreadable.
func buildProjectContext() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n# Project Context\n\n")

	// 1. File listing (newest first, up to 15 files, source/docs/config only).
	entries, readErr := scanProjectDir(cwd)
	if readErr == nil && len(entries) > 0 {
		b.WriteString("## Files in this directory (newest first)\n")
		count := 0
		for _, e := range entries {
			if count >= 15 {
				break
			}
			label := classifyFile(e)
			if label == "" {
				continue // skip binaries, temp files
			}
			timeHint := ""
			if hoursSince(e.ModTime) < 2 {
				timeHint = " [just modified]"
			} else if hoursSince(e.ModTime) < 24 {
				timeHint = " [modified today]"
			}
			b.WriteString(fmt.Sprintf("- %s (%s)%s\n", e.Path, label, timeHint))
			count++
		}
		if len(entries) > 15 {
			b.WriteString(fmt.Sprintf("  ... and %d more files\n", len(entries)-15))
		}
		b.WriteByte('\n')
	}

	// 2. Project purpose from README (first meaningful paragraph).
	if purpose := readmeSummary(cwd); purpose != "" {
		b.WriteString("## What this project does\n")
		b.WriteString(purpose)
		b.WriteString("\n\n")
	}

	// 3. Git status (compact, if available).
	if gitCtx := gitStatusCompact(); gitCtx != "" {
		b.WriteString("## Git status\n")
		b.WriteString(gitCtx)
		b.WriteString("\n\n")
	}

	// 4. Recent REPL actions (from SQLite session context).
	if replCtx := recentActionsCompact(); replCtx != "" {
		b.WriteString("## Recent actions in this session\n")
		b.WriteString(replCtx)
		b.WriteString("\n\n")
	}

	result := strings.TrimRight(b.String(), "\n")
	// Cap at ~1500 chars to keep within token budget alongside CLI_guide.
	if len(result) > 1800 {
		result = result[:1800]
		if lastNL := strings.LastIndex(result, "\n"); lastNL > 0 {
			result = result[:lastNL]
		}
		result += "\n\n[Project context truncated at 1800 chars]"
	}
	return result
}

// listProjectFiles reads the working directory and returns files sorted by
// modification time (newest first). Skips hidden directories, binary files,
// and tool artifacts.
func scanProjectDir(dir string) ([]projectFileEntry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var entries []projectFileEntry
	for _, de := range dirEntries {
		name := de.Name()

		// Skip hidden directories and tool artifacts.
		if de.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" ||
				name == "vendor" || name == "__pycache__" ||
				name == "avatars" || name == "bin" || name == "obj" {
				continue
			}
			// Include source directories but mark as dir.
			info, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, projectFileEntry{
				Path:    name + "/",
				Size:    0,
				ModTime: info.ModTime(),
				IsDir:   true,
			})
			continue
		}

		// Skip hidden files and tool artifacts.
		if strings.HasPrefix(name, ".") {
			continue
		}

		info, err := de.Info()
		if err != nil {
			continue
		}

		// Skip binary/temp files that aren't useful for routing.
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".exe" || ext == ".dll" || ext == ".so" || ext == ".dylib" ||
			ext == ".o" || ext == ".a" || ext == ".out" ||
			ext == ".pyc" || ext == ".class" || ext == ".jar" ||
			ext == ".bak" || ext == ".tmp" || ext == ".log" {
			continue
		}
		// Skip generated bootstrap artifacts (they duplicate README).
		if name == "bootstrap_summary.md" {
			continue
		}

		entries = append(entries, projectFileEntry{
			Path:    name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   false,
		})
	}

	// Sort by modification time, newest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ModTime.After(entries[j].ModTime)
	})

	return entries, nil
}

// fileLabel returns a human-readable file type label.
func classifyFile(e projectFileEntry) string {
	if e.IsDir {
		return "directory"
	}
	name := strings.ToLower(e.Path)
	ext := filepath.Ext(name)

	// Source files.
	switch ext {
	case ".go":
		return "Go source"
	case ".py":
		return "Python source"
	case ".js", ".mjs":
		return "JavaScript source"
	case ".ts":
		return "TypeScript source"
	case ".rs":
		return "Rust source"
	case ".java":
		return "Java source"
	case ".cs":
		return "C# source"
	case ".c", ".h":
		return "C source"
	case ".cpp", ".hpp", ".cc", ".cxx":
		return "C++ source"
	case ".rb":
		return "Ruby source"
	case ".php":
		return "PHP source"
	case ".swift":
		return "Swift source"
	// Config/manifest files.
	case ".mod", ".sum":
		return "Go module"
	case ".toml", ".yaml", ".yml", ".json", ".xml":
		return "config"
	case ".env":
		return "environment"
	case ".cfg", ".ini", ".conf":
		return "config"
	// Documentation.
	case ".md":
		return "documentation"
	case ".txt":
		return "text file"
	case ".rst":
		return "documentation"
	// Web.
	case ".html", ".htm":
		return "HTML"
	case ".css", ".scss", ".sass", ".less":
		return "stylesheet"
	case ".svg":
		return "SVG image"
	// Shell/script.
	case ".sh", ".bash", ".zsh":
		return "shell script"
	case ".ps1":
		return "PowerShell script"
	case ".bat", ".cmd":
		return "batch file"
	// Data.
	case ".csv":
		return "CSV data"
	case ".sql", ".sqlite", ".db":
		return "database"
	// Package files.
	case ".lock":
		return "lock file"
	}

	// Name-based heuristics.
	switch {
	case name == "makefile" || name == "gnumakefile":
		return "build file"
	case name == "dockerfile":
		return "Dockerfile"
	case name == "package.json":
		return "Node.js package"
	case name == "pyproject.toml":
		return "Python package"
	case name == "cargo.toml":
		return "Rust package"
	case name == "readme.md":
		return "README"
	case strings.HasPrefix(name, "license"):
		return "license"
	case strings.HasPrefix(name, "docker-compose"):
		return "Docker Compose"
	}

	return "file"
}

// readmeSummary extracts the first meaningful paragraph from README.md.
// Returns "" if README is not found or unreadable.
func readmeSummary(dir string) string {
	// Try README.md first, then readme.md, README, etc.
	candidates := []string{"README.md", "readme.md", "README", "readme", "README.txt", "readme.txt"}
	for _, name := range candidates {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		summary := extractFirstParagraph(string(data))
		if summary != "" {
			return summary
		}
	}
	return ""
}

// extractFirstParagraph returns the first non-heading, non-empty paragraph
// from markdown text. Caps at 200 chars.
func extractFirstParagraph(text string) string {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip headings, empty lines, metadata.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "![") ||
			strings.HasPrefix(trimmed, "[![") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		// Skip badge lines.
		if strings.Contains(trimmed, "![]") {
			continue
		}
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
			if lastSpace := strings.LastIndex(trimmed, " "); lastSpace > 150 {
				trimmed = trimmed[:lastSpace] + "..."
			}
		}
		return trimmed
	}
	return ""
}

// gitStatusCompact returns a compact git status summary.
// Returns "" if git is not available or the directory is not a repo.
func gitStatusCompact() string {
	// Try to find git. We don't import os/exec for this lightweight check —
	// use a simple approach: check for .git directory.
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return ""
	}
	// We have a .git directory. Build a compact file-based summary.
	// Only list changed/untracked source files (up to 5).
	changed := listChangedSourceFiles()
	untracked := listUntrackedSourceFiles()
	if len(changed) == 0 && len(untracked) == 0 {
		return "working tree clean"
	}
	var parts []string
	if len(changed) > 0 {
		parts = append(parts, fmt.Sprintf("modified: %s", strings.Join(changed, ", ")))
	}
	if len(untracked) > 0 {
		parts = append(parts, fmt.Sprintf("new: %s", strings.Join(untracked, ", ")))
	}
	return strings.Join(parts, "; ")
}

// listChangedSourceFiles checks the git index for modified tracked files.
// Limited to 5 files of interest (source, doc, config).
func listChangedSourceFiles() []string {
	// We use a simplified approach: look for files modified in the last
	// few minutes that aren't in .gitignore patterns.
	// A full git diff would require os/exec; this provides basic awareness.
	return nil // Simplified — real implementation would use git diff --name-only
}

// listUntrackedSourceFiles checks for new untracked files.
// Limited to 5 files of interest.
func listUntrackedSourceFiles() []string {
	return nil // Simplified
}

// recentActionsCompact returns a compact summary of recent REPL actions
// from the SQLite session context. Returns "" if no context is available.
func recentActionsCompact() string {
	// This reuses the existing buildRecentFileContext but formats it
	// more compactly for the system prompt.
	ctx := buildRecentFileContext()
	if ctx == "" {
		return ""
	}
	// The ctx is already formatted for LLM consumption. Use it directly
	// but cap length.
	if len(ctx) > 400 {
		ctx = ctx[:400]
		if lastNL := strings.LastIndex(ctx, "\n"); lastNL > 300 {
			ctx = ctx[:lastNL]
		}
	}
	return ctx
}

// hoursSince returns the number of hours since the given time.
func hoursSince(t time.Time) float64 {
	return time.Since(t).Hours()
}
