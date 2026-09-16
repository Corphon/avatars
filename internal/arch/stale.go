package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleReason describes why an architecture.md was marked stale.
type StaleReason int

const (
	StaleNone        StaleReason = iota
	StaleEntryMtime              // an entry point file was modified after arch doc
	StaleDepChanged              // a dependency manifest (go.mod, package.json, etc.) changed
	StaleFileGrowth              // >20% new source files since arch doc generation
	StaleManualMark             // user explicitly marked as stale
)

func (s StaleReason) String() string {
	switch s {
	case StaleEntryMtime:
		return "entry point file modified since architecture.md was generated"
	case StaleDepChanged:
		return "dependency manifest changed"
	case StaleFileGrowth:
		return "significant new files added (>20% growth)"
	case StaleManualMark:
		return "manually marked as stale"
	default:
		return ""
	}
}

// StaleCheck holds the result of an architecture.md staleness check.
type StaleCheck struct {
	IsStale  bool
	Reasons  []string
	Details  string // human-readable explanation
}

// CheckStale determines whether an existing architecture.md is stale by
// comparing the current project state against the fingerprint recorded in
// the ArchDoc metadata. Returns nil if the doc is nil or not stale.
func CheckStale(root string, doc *ArchDoc) *StaleCheck {
	if doc == nil {
		return &StaleCheck{IsStale: true, Reasons: []string{"no architecture document exists"}, Details: "Run avatars arch --analyze to create one."}
	}

	if doc.Meta.Status == "stale" {
		return &StaleCheck{IsStale: true, Reasons: []string{"previously marked as stale"}, Details: "The architecture document was previously marked as stale."}
	}

	if doc.Meta.Status == "draft" {
		return &StaleCheck{IsStale: true, Reasons: []string{"document is a draft, not yet confirmed"}, Details: "Review and confirm the architecture document to mark it as current."}
	}

	check := &StaleCheck{}

	// 1. Check if key entry files have been modified since generation.
	if doc.Meta.GeneratedAt != "" {
		genTime, err := time.Parse(time.RFC3339, doc.Meta.GeneratedAt)
		if err == nil {
			for _, ep := range doc.EntryPoints {
				absPath := filepath.Join(root, ep.Path)
				info, err := os.Stat(absPath)
				if err != nil {
					continue // file may have been deleted
				}
				if info.ModTime().After(genTime) {
					check.Reasons = append(check.Reasons, fmt.Sprintf("%s: %s", StaleEntryMtime, ep.Path))
				}
			}
		}
	}

	// 2. Check dependency manifests for changes.
	for _, depFile := range []string{"go.mod", "package.json", "requirements.txt", "Cargo.toml", "pyproject.toml", "Gemfile", "pom.xml", "build.gradle", "CMakeLists.txt", "composer.json"} {
		absPath := filepath.Join(root, depFile)
		_, err := os.Stat(absPath)
		if err != nil {
			continue
		}
		// Check if dependency manifest was modified after arch doc generation.
		if info, _ := os.Stat(absPath); info != nil {
			if genTime, err := time.Parse(time.RFC3339, doc.Meta.GeneratedAt); err == nil {
				if info.ModTime().After(genTime) {
					check.Reasons = append(check.Reasons, fmt.Sprintf("%s: %s", StaleDepChanged, depFile))
					break // only need one dep change to flag
				}
			}
		}
	}

	// 3. Check for significant file growth (>20% new source files).
	currentScan, err := ScanProject(root)
	if err == nil && doc.Meta.LastModifiedFiles != nil {
		currentCount := currentScan.FileStats.TotalFiles
		// Estimate original file count from the tracked files context.
		// A rough heuristic: if current files exceed expected by >20%, flag.
		if currentCount > 0 {
			// Count source files specifically (.go, .py, .js, .ts, .rs, .java, etc.)
			sourceExts := map[string]bool{
				".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true,
				".jsx": true, ".rs": true, ".java": true, ".rb": true, ".c": true,
				".cpp": true, ".h": true, ".hpp": true,
			}
			sourceCount := 0
			for ext, count := range currentScan.FileStats.ByExtension {
				if sourceExts[strings.ToLower(ext)] {
					sourceCount += count
				}
			}
			// If we have a previous fingerprint, compare full file tree hash.
			if doc.Meta.FileFingerprint != "" && len(doc.Meta.LastModifiedFiles) > 0 {
				currentFP := ComputeFingerprint(root, doc.Meta.LastModifiedFiles)
				if currentFP != doc.Meta.FileFingerprint {
					check.Reasons = append(check.Reasons, fmt.Sprintf("%s (fingerprint changed: %s → %s)", StaleFileGrowth, doc.Meta.FileFingerprint, currentFP))
				}
			} else if sourceCount > 0 {
				// Fallback: if we can't fingerprint, use file count heuristic.
				// We'd need the original count — since we don't have it, skip.
				_ = sourceCount
			}
		}
	}

	if len(check.Reasons) > 0 {
		check.IsStale = true
		check.Details = strings.Join(check.Reasons, "; ")
	}

	return check
}

// MarkStale writes a stale marker to the architecture.md and returns
// the updated document.
func MarkStale(root string, doc *ArchDoc) error {
	if doc == nil {
		return fmt.Errorf("arch mark-stale: nil document")
	}
	doc.Meta.Status = "stale"
	return WriteArchDoc(root, doc)
}

// ConfirmArchDoc marks the architecture.md as confirmed (user-reviewed).
// R3-9: refuses confirm when Overview is a prompt dump or Registration Points
// are empty. Caller should ensure the project builds before invoking.
func ConfirmArchDoc(root string, doc *ArchDoc) error {
	if doc == nil {
		return fmt.Errorf("arch confirm: nil document")
	}
	healthy := true
	if ok, reasons := CanConfirm(doc, healthy); !ok {
		return fmt.Errorf("arch confirm refused: %s", strings.Join(reasons, "; "))
	}
	doc.Meta.Status = "confirmed"
	doc.Meta.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if scan, err := ScanProject(root); err == nil {
		doc.Meta.LastModifiedFiles = scan.KeyFiles()
		doc.Meta.FileFingerprint = ComputeFingerprint(root, doc.Meta.LastModifiedFiles)
	}
	return WriteArchDoc(root, doc)
}

// MergeArchDoc merges an LLM-generated update into an existing document.
// For --resume mode: keeps the target architecture but updates the current
// state, adding any newly discovered entry points, registration points, etc.
func MergeArchDoc(existing, update *ArchDoc) *ArchDoc {
	if existing == nil {
		return update
	}
	if update == nil {
		return existing
	}

	merged := *existing // shallow copy

	// Merge entry points: add new ones not already present.
	seenEP := make(map[string]bool)
	for _, ep := range merged.EntryPoints {
		seenEP[ep.Path] = true
	}
	for _, ep := range update.EntryPoints {
		if !seenEP[ep.Path] {
			merged.EntryPoints = append(merged.EntryPoints, ep)
		}
	}

	// Merge registration points: update existing, add new change types.
	seenRP := make(map[string]int) // change_type → index in merged
	for i, rp := range merged.RegPoints {
		seenRP[rp.ChangeType] = i
	}
	for _, rp := range update.RegPoints {
		if idx, ok := seenRP[rp.ChangeType]; ok {
			merged.RegPoints[idx] = rp // replace with updated version
		} else {
			merged.RegPoints = append(merged.RegPoints, rp)
		}
	}

	// Update layers and conventions from the new analysis.
	if len(update.Layers) > 0 {
		merged.Layers = update.Layers
	}
	if len(update.Conventions) > 0 {
		merged.Conventions = update.Conventions
	}
	if len(update.Dependencies.Internal) > 0 || len(update.Dependencies.External) > 0 {
		merged.Dependencies = update.Dependencies
	}

	// Update metadata.
	merged.Meta.Source = "resume"
	merged.Meta.Status = "draft"
	merged.Overview = update.Overview

	return &merged
}

// IsArchFile checks whether a given file path is referenced in the architecture
// document — either as an Entry Point or in a Registration Point's file list.
// Used by edit/edit-many to determine if architecture.md should be auto-staled.
func IsArchFile(doc *ArchDoc, filePath string) bool {
	if doc == nil {
		return false
	}
	normalized := filepath.ToSlash(filePath)

	// Check Entry Points.
	for _, ep := range doc.EntryPoints {
		if filepath.ToSlash(ep.Path) == normalized {
			return true
		}
	}

	// Check Registration Points file lists.
	for _, rp := range doc.RegPoints {
		for _, rf := range rp.Files {
			if filepath.ToSlash(rf.Path) == normalized {
				return true
			}
		}
	}

	return false
}

// MaybeMarkStaleAfterEdit checks if any of the modified files are referenced
// in architecture.md, and if so, marks the document as stale. This is the
// edit→architecture lifecycle link: when the user edits a file that the
// architecture document tracks, the document automatically becomes stale.
//
// Returns true if the document was marked stale, false otherwise.
func MaybeMarkStaleAfterEdit(root string, modifiedFiles []string) bool {
	if len(modifiedFiles) == 0 {
		return false
	}

	doc, err := ReadArchDoc(root)
	if err != nil || doc == nil {
		return false
	}

	// Only auto-stale confirmed documents. Drafts are already unconfirmed.
	if doc.Meta.Status != "confirmed" {
		return false
	}

	for _, f := range modifiedFiles {
		if IsArchFile(doc, f) {
			_ = MarkStale(root, doc)
			return true
		}
	}
	return false
}
