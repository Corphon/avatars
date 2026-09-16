// Package arch provides the architecture.md engine — a project-level
// architecture understanding layer that is project-type-agnostic.
//
// The engine scans any project directory, extracts a structured summary of
// files, entry points, dependency manifests, and configuration files, then
// delegates to an LLM for semantic interpretation (layers, registration
// points, conventions). The resulting architecture.md is a persistent asset
// that all downstream components (NL router, Planner, Builder) can consume.
//
// Design principles:
//  1. Zero hardcoded language/framework assumptions — the scanner is pure
//     heuristics, the LLM does all semantic interpretation.
//  2. The engine produces structured input for the LLM; the LLM produces
//     structured output (ArchDoc) that the engine persists.
//  3. Architecture.md is a project asset, not a runtime cache — it's meant
//     to be reviewed, edited, and committed.
package arch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"avatars/internal/projectfiles"
)

// ArchDoc is the in-memory representation of an architecture.md file.
// It is both the output of LLM analysis and the input for injection adapters.
type ArchDoc struct {
	Meta         ArchMeta            `json:"meta"`
	Overview     string              `json:"overview"`
	EntryPoints  []EntryPoint        `json:"entry_points"`
	Layers       []Layer             `json:"layers"`
	RegPoints    []RegistrationPoint `json:"registration_points"`
	Conventions  []Convention        `json:"conventions"`
	Dependencies ArchDependencies    `json:"dependencies"`
	DataFlow     *DataFlowReport     `json:"data_flow,omitempty"`
}

// ArchMeta carries provenance and staleness metadata.
type ArchMeta struct {
	GeneratedAt       string   `json:"generated_at"`
	Source            string   `json:"source"` // init, analyze, resume
	Status            string   `json:"status"` // draft, confirmed, stale
	FileFingerprint   string   `json:"file_fingerprint"`
	LastModifiedFiles []string `json:"last_modified_files"`
}

// EntryPoint describes how execution enters the project.
type EntryPoint struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`    // e.g. "http-server", "cli", "library", "script"
	Summary string `json:"summary"` // one-line description
}

// Layer describes a logical layer in the project architecture.
type Layer struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"` // files/directories belonging to this layer
}

// RegistrationPoint describes a location where coordinated changes are needed
// when adding a new component of a certain type. This is the generic version
// of "where to add a provider" — it captures any multi-file change pattern.
type RegistrationPoint struct {
	ChangeType string    `json:"change_type"` // e.g. "add-provider", "add-route", "add-command"
	Files      []RegFile `json:"files"`       // all files that must be modified
	Note       string    `json:"note"`        // human explanation of the pattern
}

// RegFile is one file involved in a registration point.
type RegFile struct {
	Path       string `json:"path"`
	LineRange  string `json:"line_range"`  // e.g. "L234-L280" or ""
	ChangeHint string `json:"change_hint"` // e.g. "append to providers map"
	Example    string `json:"example"`     // e.g. `"new-provider": NewProvider()`
}

// Convention describes a cross-cutting rule or pattern.
type Convention struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Mandatory   bool   `json:"mandatory"`
}

// ArchDependencies captures internal and external dependency relationships.
type ArchDependencies struct {
	Internal []string `json:"internal"` // e.g. "internal/config → internal/platform"
	External []string `json:"external"` // e.g. "github.com/spf13/cobra (CLI framework)"
}

// ProjectScan is the raw, uninterpreted output of scanning a project directory.
// It is passed to the LLM for semantic interpretation. This structure is
// deliberately language-agnostic — no assumptions about Go/Python/JS/etc.
type ProjectScan struct {
	Root            string           `json:"root"`
	FileTree        []string         `json:"file_tree"`        // relative paths, sorted
	EntryCandidates []EntryCandidate `json:"entry_candidates"` // files that look like entry points
	DepManifests    []DepManifest    `json:"dep_manifests"`    // dependency declaration files
	ConfigFiles     []string         `json:"config_files"`     // configuration files
	DocFiles        []string         `json:"doc_files"`        // documentation files
	KeyDirNames     []string         `json:"key_dir_names"`    // top-level directories
	FileStats       FileStats        `json:"file_stats"`       // counts by extension
}

// EntryCandidate is a file that looks like an entry point based on heuristics.
type EntryCandidate struct {
	Path    string `json:"path"`
	Reason  string `json:"reason"`  // e.g. "contains func main", "package main", "if __name__"
	Content string `json:"content"` // first ~200 chars for LLM context
}

// DepManifest is a discovered dependency declaration file.
type DepManifest struct {
	Path    string `json:"path"`    // e.g. "go.mod", "package.json"
	Kind    string `json:"kind"`    // e.g. "go-mod", "npm", "pip", "cargo"
	Content string `json:"content"` // first ~500 chars for LLM context
}

// FileStats records file counts by extension.
type FileStats struct {
	ByExtension map[string]int `json:"by_extension"`
	TotalFiles  int            `json:"total_files"`
}

// ScanProject walks a project directory and produces a ProjectScan — a
// structured but uninterpreted summary. The LLM does the interpretation.
// This is the primary input for --analyze and --resume modes.
func ScanProject(root string) (*ProjectScan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("arch scan: abs: %w", err)
	}

	scan := &ProjectScan{
		Root:      root,
		FileStats: FileStats{ByExtension: make(map[string]int)},
	}

	// Collect file tree and statistics.
	var files []string
	topDirs := make(map[string]bool)

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}

		// Skip well-known noise directories.
		if info.IsDir() {
			name := info.Name()
			lowerName := strings.ToLower(name)
			if projectfiles.ShouldSkipWalkDir(name) || name == "stage" || name == "web" ||
				name == ".idea" || name == ".vscode" || name == ".pytest_cache" ||
				strings.HasSuffix(lowerName, ".egg-info") ||
				strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			// Track top-level directories — skip pollution / cache-like names.
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) == 1 && rel != "." {
				if !isNoiseTopDir(name) {
					topDirs[rel] = true
				}
			}
			return nil
		}

		if isHarnessRuntimeArtifact(info.Name()) {
			return nil
		}

		files = append(files, rel)
		ext := strings.ToLower(filepath.Ext(rel))
		if ext == "" {
			ext = "(no extension)"
		}
		scan.FileStats.ByExtension[ext]++
		scan.FileStats.TotalFiles++

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("arch scan: walk: %w", err)
	}

	sort.Strings(files)
	scan.FileTree = files

	for d := range topDirs {
		scan.KeyDirNames = append(scan.KeyDirNames, d)
	}
	sort.Strings(scan.KeyDirNames)

	// Discover entry point candidates, dependency manifests, config files, and docs.
	depManifestNames := map[string]string{
		"go.mod": "go-mod", "go.sum": "go-sum",
		"package.json": "npm", "package-lock.json": "npm-lock",
		"requirements.txt": "pip", "Pipfile": "pipfile", "pyproject.toml": "pyproject",
		"Cargo.toml": "cargo", "Cargo.lock": "cargo-lock",
		"Gemfile": "gem", "pom.xml": "maven", "build.gradle": "gradle",
		"CMakeLists.txt": "cmake", "Makefile": "makefile",
		"tsconfig.json": "tsconfig", "vite.config.ts": "vite",
		"composer.json": "composer",
	}
	configExtensions := map[string]bool{
		".yaml": true, ".yml": true, ".json": true, ".toml": true,
		".env": true, ".ini": true, ".cfg": true, ".conf": true,
	}
	docExtensions := map[string]bool{
		".md": true, ".rst": true, ".txt": true, ".adoc": true,
	}

	for _, f := range files {
		base := filepath.Base(f)
		ext := strings.ToLower(filepath.Ext(f))

		// Dependency manifests.
		if kind, ok := depManifestNames[base]; ok {
			dm := DepManifest{Path: f, Kind: kind}
			dm.Content = readFirstN(filepath.Join(root, f), 500)
			scan.DepManifests = append(scan.DepManifests, dm)
		}

		// Config files (heuristic: config-like extensions in root or config/ dirs).
		if configExtensions[ext] {
			dir := filepath.Dir(f)
			if dir == "." || strings.HasPrefix(dir, "config") || strings.HasPrefix(dir, ".github") {
				scan.ConfigFiles = append(scan.ConfigFiles, f)
			}
		}

		// Documentation files.
		if docExtensions[ext] {
			scan.DocFiles = append(scan.DocFiles, f)
		}

		// Entry point candidates: files in cmd/, main.*, or with known patterns.
		if isEntryCandidate(f, base) {
			ec := EntryCandidate{
				Path:   f,
				Reason: entryReason(f, base),
			}
			ec.Content = readFirstN(filepath.Join(root, f), 200)
			scan.EntryCandidates = append(scan.EntryCandidates, ec)
		}
	}

	sort.Slice(scan.DepManifests, func(i, j int) bool { return scan.DepManifests[i].Path < scan.DepManifests[j].Path })
	sort.Slice(scan.EntryCandidates, func(i, j int) bool { return scan.EntryCandidates[i].Path < scan.EntryCandidates[j].Path })

	return scan, nil
}

// isEntryCandidate uses language-agnostic heuristics to guess whether a file
// is an entry point. The LLM makes the final determination.
func isEntryCandidate(path, base string) bool {
	dir := filepath.Dir(path)

	// Directories that conventionally contain entry points.
	if strings.HasPrefix(dir, "cmd") || strings.HasPrefix(dir, "cmd/") {
		return true
	}

	// Common entry-point file names.
	entryNames := map[string]bool{
		"main.go": true, "main.py": true, "main.rs": true, "main.ts": true,
		"main.js": true, "main.c": true, "main.cpp": true,
		"index.ts": true, "index.js": true, "index.tsx": true,
		"app.go": true, "app.py": true, "server.go": true, "server.py": true,
		"cli.go": true, "cli.py": true, "run.go": true, "run.py": true,
		"__main__.py": true, "setup.py": true,
	}
	return entryNames[base]
}

func entryReason(path, base string) string {
	dir := filepath.Dir(path)
	if strings.HasPrefix(dir, "cmd") {
		return "in cmd/ directory"
	}
	switch filepath.Ext(base) {
	case ".go":
		return "potential Go main package"
	case ".py":
		return "potential Python entry script"
	case ".rs":
		return "potential Rust binary"
	case ".ts", ".js", ".tsx", ".jsx":
		return "potential JS/TS entry point"
	default:
		return "matches entry-point naming convention"
	}
}

// isNoiseTopDir filters pollution / cache / packaging dirs from architecture Layers.
func isNoiseTopDir(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "conftest", "stage", "web", "__pycache__", ".pytest_cache",
		"node_modules", "vendor", "dist", "build", "target", "coverage":
		return true
	}
	if strings.HasSuffix(lower, ".egg-info") {
		return true
	}
	if projectfiles.ShouldSkipWalkDir(name) {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

func isHarnessRuntimeArtifact(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "_") {
		if strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".pid") ||
			strings.HasSuffix(lower, ".txt") {
			return true
		}
	}
	return strings.HasSuffix(lower, ".err.log")
}

func readFirstN(absPath string, n int) string {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	s := string(data)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ReadArchDoc reads and parses an existing architecture.md file.
// Returns nil if the file doesn't exist or can't be parsed.
func ReadArchDoc(root string) (*ArchDoc, error) {
	archPath := filepath.Join(root, "architecture.md")
	data, err := os.ReadFile(archPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("arch read: %w", err)
	}
	return ParseArchDocFromMarkdown(string(data))
}

// EnsureArchitectureDoc creates architecture.md if it doesn't exist.
// For new/empty projects, uses --init mode (design from intent).
// For existing projects, uses --analyze mode (reverse-engineer).
// No user interaction — designed for workflow automation.
//
// R3-9: Overview is sanitized (no prompt dump); status stays draft until
// Registration Points exist and (on later refresh) the project is healthy.
func isNonProjectTaskPlaceholder(taskDesc string) bool {
	t := strings.ToLower(strings.TrimSpace(taskDesc))
	return t == "summarize this attached file" || t == "看看这个文件"
}

func EnsureArchitectureDoc(root string, taskDesc string) error {
	if isNonProjectTaskPlaceholder(taskDesc) {
		return nil
	}
	// Skip if already exists.
	if existing, _ := ReadArchDoc(root); existing != nil {
		return nil
	}

	scan, err := ScanProject(root)
	if err != nil {
		scan = &ProjectScan{Root: root, FileStats: FileStats{ByExtension: make(map[string]int)}}
	}

	mode := "analyze"
	if scan.FileStats.TotalFiles == 0 {
		mode = "init"
		scan = NewScanForInit(root)
	}

	doc := NewArchDoc("workflow-auto-"+mode, root, scan.KeyFiles())
	_ = RefreshArchDocFromProject(root, doc, RefreshOptions{
		PreserveOverview: false,
		TaskDesc:         taskDesc,
		HealthKnown:      false, // pre-plan — do not invent confirmed
	})
	doc.Meta.Source = "workflow-auto-" + mode
	return WriteArchDoc(root, doc)
}

// WriteArchDoc serializes an ArchDoc to architecture.md on disk.
func WriteArchDoc(root string, doc *ArchDoc) error {
	if doc == nil {
		return fmt.Errorf("arch write: nil document")
	}
	archPath := filepath.Join(root, "architecture.md")
	content := FormatArchDoc(doc)
	return os.WriteFile(archPath, []byte(content), 0644)
}

// ComputeFingerprint computes a content hash over key files for staleness detection.
func ComputeFingerprint(root string, files []string) string {
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// NewArchDoc creates an empty ArchDoc with current timestamp and fingerprint.
func NewArchDoc(source string, root string, scannedFiles []string) *ArchDoc {
	return &ArchDoc{
		Meta: ArchMeta{
			GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
			Source:            source,
			Status:            "draft",
			FileFingerprint:   ComputeFingerprint(root, scannedFiles),
			LastModifiedFiles: scannedFiles,
		},
	}
}

// NewScanForInit creates a minimal ProjectScan for --init mode where there's
// little to no existing code. The user's description provides the seed.
func NewScanForInit(root string) *ProjectScan {
	// In --init mode we still scan whatever exists (even if nearly empty).
	scan, err := ScanProject(root)
	if err != nil {
		scan = &ProjectScan{Root: root, FileStats: FileStats{ByExtension: make(map[string]int)}}
	}
	return scan
}

// KeyFiles returns the set of "key files" that should be tracked for staleness.
// These are entry points, dependency manifests, and config files.
func (s *ProjectScan) KeyFiles() []string {
	var files []string
	for _, ec := range s.EntryCandidates {
		files = append(files, ec.Path)
	}
	for _, dm := range s.DepManifests {
		files = append(files, dm.Path)
	}
	files = append(files, s.ConfigFiles...)
	sort.Strings(files)
	return files
}
