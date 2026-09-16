package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"avatars/internal/skillbuilder"
)

type Store struct {
	root string
}

func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) Generate(runID string, taskID string, proposal skillbuilder.Proposal) (string, error) {
	prepared, err := s.PrepareGenerated(runID, taskID, proposal)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(prepared.Path, []byte(prepared.Content), 0o644); err != nil {
		return "", fmt.Errorf("write generated skill: %w", err)
	}
	if err := s.FinalizeGenerated(prepared.Path, prepared.GeneratedAt); err != nil {
		return "", err
	}
	return prepared.Path, nil
}

func (s *Store) PrepareGenerated(runID string, taskID string, proposal skillbuilder.Proposal) (PreparedGeneratedSkill, error) {
	if s == nil {
		return PreparedGeneratedSkill{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return PreparedGeneratedSkill{}, err
	}
	fileName := fmt.Sprintf("%s-%s-%s.md", sanitizeFileName(runID), sanitizeFileName(taskID), proposal.Slug)
	path := filepath.Join(s.generatedDir(), fileName)
	generatedAt := time.Now().UTC()
	content, err := rewriteFrontmatterScalars(proposal.Markdown(), map[string]string{
		"lifecycle-state": "generated",
		"source-run-id":   runID,
		"source-task-id":  taskID,
		"generated-at":    generatedAt.Format(time.RFC3339),
	})
	if err != nil {
		return PreparedGeneratedSkill{}, fmt.Errorf("prepare generated skill metadata: %w", err)
	}
	return PreparedGeneratedSkill{Path: path, Content: content, GeneratedAt: generatedAt}, nil
}

func (s *Store) FinalizeGenerated(path string, generatedAt time.Time) error {
	if s == nil {
		return errors.New("skills store is nil")
	}
	resolvedPath, err := s.resolveGeneratedPath(path)
	if err != nil {
		return err
	}
	history, err := readSkillHistory(resolvedPath)
	if err != nil {
		return err
	}
	if len(history) > 0 {
		return nil
	}
	definition, err := s.readDefinition(resolvedPath)
	if err != nil {
		return err
	}
	if generatedAt.IsZero() {
		generatedAt = definition.GeneratedAt
		if generatedAt.IsZero() {
			generatedAt = time.Now().UTC()
		}
	}
	if err := writeSkillHistory(resolvedPath, []SkillHistoryEntry{{At: generatedAt, Action: "generate", ToState: "generated", Path: resolvedPath}}); err != nil {
		return fmt.Errorf("write generated skill history: %w", err)
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(definition, "generate", "", "generated", resolvedPath, generatedAt)); err != nil {
		return fmt.Errorf("write generated skill ledger entry: %w", err)
	}
	return nil
}

// ShouldAutoApprove checks the governance ledger for a generated skill's
// task_completed entries. K3: Role-based thresholds — Builder/Critic require
// higher success rates than Researcher/Planner.
func (s *Store) ShouldAutoApprove(skillName string) bool {
	entries, err := s.readGovernanceLedgerEntries()
	if err != nil {
		return false
	}
	// K3: Determine role from skill definition for per-role thresholds.
	role := s.inferSkillRole(skillName)
	minTasks, minRate := roleThresholds(role)

	var success, failure int
	for _, e := range entries {
		if e.Action == "task_completed" && e.SkillName == skillName {
			switch e.ToState {
			case "success":
				success++
			case "failure":
				failure++
			}
		}
	}
	total := success + failure
	if total < minTasks {
		return false
	}
	return float64(success)/float64(total) >= minRate
}

// inferSkillRole reads the role frontmatter from a skill definition. K3.
func (s *Store) inferSkillRole(skillName string) string {
	def, err := s.LoadApproved(skillName)
	if err != nil {
		return "builder" // default strict
	}
	if def.Role != "" {
		return def.Role
	}
	if strings.Contains(def.TemplateID, "builder") {
		return "builder"
	}
	if strings.Contains(def.TemplateID, "researcher") {
		return "researcher"
	}
	if strings.Contains(def.TemplateID, "critic") {
		return "critic"
	}
	return "builder"
}

// roleThresholds returns (minTasks, minSuccessRate) for auto-approval. K3.
func roleThresholds(role string) (int, float64) {
	switch strings.ToLower(role) {
	case "builder", "critic":
		return 7, 0.85 // strict: code-gen skills are high-stakes
	case "researcher":
		return 3, 0.65 // lenient: surveys are lower-stakes
	case "planner", "synthesizer":
		return 5, 0.75 // balanced
	default:
		return 5, 0.80
	}
}

func (s *Store) Approve(candidatePath string) (string, error) {
	if s == nil {
		return "", errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return "", err
	}

	resolvedPath, err := s.resolveGeneratedPath(candidatePath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read generated skill %s: %w", resolvedPath, err)
	}
	approvedAt := time.Now().UTC()
	updatedContent, err := rewriteFrontmatterScalars(string(content), map[string]string{
		"lifecycle-state": "approved",
		"approved-at":     approvedAt.Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("prepare approved skill metadata: %w", err)
	}
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0o644); err != nil {
		return "", fmt.Errorf("persist approved skill metadata: %w", err)
	}

	approvedPath := filepath.Join(s.approvedDir(), filepath.Base(resolvedPath))
	if err := os.Rename(resolvedPath, approvedPath); err != nil {
		return "", fmt.Errorf("approve skill: %w", err)
	}
	if err := appendSkillHistory(resolvedPath, approvedPath, SkillHistoryEntry{At: approvedAt, Action: "approve", FromState: "generated", ToState: "approved", Path: approvedPath}); err != nil {
		return "", fmt.Errorf("record skill approval history: %w", err)
	}
	definition, err := s.readDefinition(approvedPath)
	if err != nil {
		return "", err
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(definition, "approve", "generated", "approved", approvedPath, approvedAt)); err != nil {
		return "", fmt.Errorf("record skill approval ledger entry: %w", err)
	}
	return approvedPath, nil
}

// Import reads an external skill markdown file from any path, validates its
// frontmatter, and imports it directly into skills/approved/. This bypasses
// the generate→approve pipeline for externally-authored skill definitions.
// If the file lacks required frontmatter fields, reasonable defaults are added.
func (s *Store) Import(sourcePath string) (string, error) {
	if s == nil {
		return "", errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return "", err
	}

	sourcePath = filepath.Clean(sourcePath)
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read source skill %s: %w", sourcePath, err)
	}

	// Validate YAML frontmatter exists.
	trimmed := strings.TrimSpace(string(content))
	if !strings.HasPrefix(trimmed, "---") {
		return "", fmt.Errorf("skill file must have YAML frontmatter (starting with ---)")
	}

	importedAt := time.Now().UTC()
	// Add/update lifecycle metadata and ensure required Listing fields exist.
	scalars := map[string]string{
		"lifecycle-state": "approved",
		"approved-at":     importedAt.Format(time.RFC3339),
	}
	// If the file lacks required Listing fields, add sensible defaults
	// so the skill passes validation after import.
	if !frontmatterHasKey(string(content), "when_to_use") && !frontmatterHasKey(string(content), "when-to-use") {
		scalars["when_to_use"] = "When the user requests web animation, SVG, Canvas, or visual effects generation."
	}
	updatedContent, err := rewriteFrontmatterScalars(string(content), scalars)
	if err != nil {
		return "", fmt.Errorf("prepare imported skill metadata: %w", err)
	}

	baseName := filepath.Base(sourcePath)
	approvedPath := filepath.Join(s.approvedDir(), baseName)

	// If a file with the same name already exists in approved, don't overwrite.
	if _, statErr := os.Stat(approvedPath); statErr == nil {
		return "", fmt.Errorf("approved skill %s already exists; archive or remove it first", baseName)
	}

	if err := os.WriteFile(approvedPath, []byte(updatedContent), 0o644); err != nil {
		return "", fmt.Errorf("write imported skill: %w", err)
	}

	if err := appendSkillHistory(sourcePath, approvedPath, SkillHistoryEntry{
		At: importedAt, Action: "import", FromState: "external", ToState: "approved", Path: approvedPath,
	}); err != nil {
		return "", fmt.Errorf("record skill import history: %w", err)
	}

	definition, err := s.readDefinition(approvedPath)
	if err != nil {
		return "", err
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(
		definition, "import", "external", "approved", approvedPath, importedAt,
	)); err != nil {
		return "", fmt.Errorf("record skill import ledger entry: %w", err)
	}

	return approvedPath, nil
}

// NavigatorSkillFilename is the filename of the always-on skills navigator index.
const NavigatorSkillFilename = "_Navigator_Skill.md"

// RegenerateNavigator rebuilds the navigator skill index by listing all
// approved skills and writing an updated _Navigator_Skill.md file.
// This is called automatically after Approve() to keep the index current.
// If the navigator file does not exist yet, it is created.
// If it already exists, its table section is replaced with the current listing.
func (s *Store) RegenerateNavigator() error {
	if s == nil {
		return errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return err
	}

	// List all approved skills.
	scan, err := s.ListApprovedTolerant()
	if err != nil {
		return fmt.Errorf("list approved skills for navigator: %w", err)
	}

	// Build the table rows.
	var rows strings.Builder
	for i, listing := range scan.Listings {
		alwaysOn := "no"
		if listing.AlwaysOn {
			alwaysOn = "yes"
		}
		// Truncate description to first sentence for the table.
		desc := strings.TrimSpace(listing.Description)
		if idx := strings.Index(desc, ". "); idx > 0 && idx < 120 {
			desc = desc[:idx+1]
		}
		if len(desc) > 150 {
			desc = desc[:147] + "..."
		}
		fmt.Fprintf(&rows, "| %d | %s | %s | %s | %s |\n",
			i+1, listing.Name, listing.Path, alwaysOn, desc)
	}

	// Build the full navigator content.
	content := buildNavigatorContent(rows.String())

	navPath := filepath.Join(s.approvedDir(), NavigatorSkillFilename)
	if err := os.WriteFile(navPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write navigator skill: %w", err)
	}
	return nil
}

func buildNavigatorContent(tableRows string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: skills-navigator\n")
	b.WriteString("description: >\n")
	b.WriteString("  Always-on index of all approved skills. Lists every skill's name, path,\n")
	b.WriteString("  function summary, and trigger conditions so the avatar LLM knows which\n")
	b.WriteString("  skills are available and when to use them. Updated automatically when\n")
	b.WriteString("  new skills are approved.\n")
	b.WriteString("when_to_use: Always active. Consult this index before proposing new skills or when the user asks what avatars can do.\n")
	b.WriteString("context: Master registry of all approved skills. Read-only reference for skill discovery and routing.\n")
	b.WriteString("version: 0.1.0\n")
	b.WriteString("lifecycle-state: approved\n")
	b.WriteString("user-invocable: false\n")
	b.WriteString("disable-model-invocation: true\n")
	b.WriteString("always-on: true\n")
	b.WriteString("allowed-tools:\n")
	b.WriteString("  - read\n")
	b.WriteString("---\n\n")
	b.WriteString("# Skills Navigator\n\n")
	b.WriteString("## Approved Skills Index\n\n")
	b.WriteString("| # | Name | Path | Always-On | Summary |\n")
	b.WriteString("|---|------|------|-----------|---------|\n")
	b.WriteString(tableRows)
	b.WriteString("\n## How to Use This Index\n\n")
	b.WriteString("1. **Before proposing a new skill**: Check this index to avoid duplicating an existing skill.\n")
	b.WriteString("2. **When the user asks what avatars can do**: Reference this index to list available capabilities.\n")
	b.WriteString("3. **When routing a request**: Check always-on skills (intent-routing, plan-mode) for routing guidance.\n")
	b.WriteString("4. **When a new skill is approved**: This index is automatically updated by the `Approve` workflow.\n")
	return b.String()
}

func (s *Store) Archive(approvedPath string) (string, error) {
	if s == nil {
		return "", errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return "", err
	}

	resolvedPath, err := s.resolveApprovedPath(approvedPath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read approved skill %s: %w", resolvedPath, err)
	}
	archivedAt := time.Now().UTC()
	updatedContent, err := rewriteFrontmatterScalars(string(content), map[string]string{
		"lifecycle-state": "archived",
		"archived-at":     archivedAt.Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("prepare archived skill metadata: %w", err)
	}
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0o644); err != nil {
		return "", fmt.Errorf("persist archived skill metadata: %w", err)
	}

	archivedPath := filepath.Join(s.archiveDir(), filepath.Base(resolvedPath))
	if err := os.Rename(resolvedPath, archivedPath); err != nil {
		return "", fmt.Errorf("archive skill: %w", err)
	}
	if err := appendSkillHistory(resolvedPath, archivedPath, SkillHistoryEntry{At: archivedAt, Action: "archive", FromState: "approved", ToState: "archived", Path: archivedPath}); err != nil {
		return "", fmt.Errorf("record skill archive history: %w", err)
	}
	definition, err := s.readDefinition(archivedPath)
	if err != nil {
		return "", err
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(definition, "archive", "approved", "archived", archivedPath, archivedAt)); err != nil {
		return "", fmt.Errorf("record skill archive ledger entry: %w", err)
	}
	return archivedPath, nil
}

func (s *Store) Disable(approvedPath string) (string, error) {
	if s == nil {
		return "", errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return "", err
	}

	resolvedPath, err := s.resolveApprovedPath(approvedPath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read approved skill %s: %w", resolvedPath, err)
	}
	disabledAt := time.Now().UTC()
	updatedContent, err := rewriteFrontmatterScalars(string(content), map[string]string{
		"lifecycle-state": "disabled",
		"disabled-at":     disabledAt.Format(time.RFC3339),
		"archived-at":     "",
	})
	if err != nil {
		return "", fmt.Errorf("prepare disabled skill metadata: %w", err)
	}
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0o644); err != nil {
		return "", fmt.Errorf("persist disabled skill metadata: %w", err)
	}

	disabledPath := filepath.Join(s.archiveDir(), filepath.Base(resolvedPath))
	if err := os.Rename(resolvedPath, disabledPath); err != nil {
		return "", fmt.Errorf("disable skill: %w", err)
	}
	if err := appendSkillHistory(resolvedPath, disabledPath, SkillHistoryEntry{At: disabledAt, Action: "disable", FromState: "approved", ToState: "disabled", Path: disabledPath}); err != nil {
		return "", fmt.Errorf("record skill disable history: %w", err)
	}
	definition, err := s.readDefinition(disabledPath)
	if err != nil {
		return "", err
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(definition, "disable", "approved", "disabled", disabledPath, disabledAt)); err != nil {
		return "", fmt.Errorf("record skill disable ledger entry: %w", err)
	}
	return disabledPath, nil
}

func (s *Store) Restore(archivedPath string) (string, error) {
	if s == nil {
		return "", errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return "", err
	}

	resolvedPath, err := s.resolveArchivePath(archivedPath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read archived skill %s: %w", resolvedPath, err)
	}
	definition, err := s.readDefinition(resolvedPath)
	if err != nil {
		return "", err
	}
	restoredAt := time.Now().UTC()
	updatedContent, err := rewriteFrontmatterScalars(string(content), map[string]string{
		"lifecycle-state": "approved",
		"archived-at":     "",
		"disabled-at":     "",
	})
	if err != nil {
		return "", fmt.Errorf("prepare restored skill metadata: %w", err)
	}
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0o644); err != nil {
		return "", fmt.Errorf("persist restored skill metadata: %w", err)
	}

	restoredPath := filepath.Join(s.approvedDir(), filepath.Base(resolvedPath))
	if err := os.Rename(resolvedPath, restoredPath); err != nil {
		return "", fmt.Errorf("restore skill: %w", err)
	}
	if err := appendSkillHistory(resolvedPath, restoredPath, SkillHistoryEntry{At: restoredAt, Action: "restore", FromState: definition.LifecycleState, ToState: "approved", Path: restoredPath}); err != nil {
		return "", fmt.Errorf("record skill restore history: %w", err)
	}
	restoredDefinition, err := s.readDefinition(restoredPath)
	if err != nil {
		return "", err
	}
	if err := s.appendGovernanceLedgerEntry(governanceLedgerEntryFromDefinition(restoredDefinition, "restore", definition.LifecycleState, "approved", restoredPath, restoredAt)); err != nil {
		return "", fmt.Errorf("record skill restore ledger entry: %w", err)
	}
	return restoredPath, nil
}

func (s *Store) ListApproved() ([]Listing, error) {
	if s == nil {
		return nil, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}

	return s.listByDir(s.approvedDir(), "approved", "")
}

func (s *Store) ListApprovedTolerant() (ListingScan, error) {
	if s == nil {
		return ListingScan{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return ListingScan{}, err
	}
	return s.listByDirTolerant(s.approvedDir(), "approved", "")
}

// CleanupStaleGenerated archives generated skills that are older than
// maxAgeDays. Returns the count of archived skills. Skills with parse
// errors are skipped (logged but not blocking).
func (s *Store) CleanupStaleGenerated(maxAgeDays int) (int, error) {
	if s == nil {
		return 0, errors.New("skills store is nil")
	}
	if maxAgeDays <= 0 {
		maxAgeDays = 7
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays)
	listings, err := s.ListGenerated()
	if err != nil {
		return 0, err
	}
	archived := 0
	for _, listing := range listings {
		if listing.GeneratedAt.IsZero() || listing.GeneratedAt.After(cutoff) {
			continue
		}
		if _, err := s.Archive(listing.Path); err != nil {
			continue // skip problematic files
		}
		archived++
	}
	return archived, nil
}

func (s *Store) ListGenerated() ([]Listing, error) {
	if s == nil {
		return nil, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}
	return s.listByDir(s.generatedDir(), "generated", "")
}

func (s *Store) ListArchived() ([]Listing, error) {
	if s == nil {
		return nil, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}
	return s.listByDir(s.archiveDir(), "archived", "archived")
}

func (s *Store) ListDisabled() ([]Listing, error) {
	if s == nil {
		return nil, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}
	return s.listByDir(s.archiveDir(), "archived", "disabled")
}

func (s *Store) LoadGenerated(generatedPath string) (Definition, error) {
	if s == nil {
		return Definition{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return Definition{}, err
	}

	resolvedPath, err := s.resolveGeneratedPath(generatedPath)
	if err != nil {
		return Definition{}, err
	}
	return s.readDefinition(resolvedPath)
}

func (s *Store) ReviewGenerated(generatedPath string) (CandidateReview, error) {
	candidate, err := s.LoadGenerated(generatedPath)
	if err != nil {
		return CandidateReview{}, err
	}
	review := CandidateReview{
		Candidate:          candidate,
		CandidateBodyLines: countTextLines(candidate.Body),
	}
	review.ValidationFindings = validateGeneratedCandidate(candidate)
	review.ReadyForApproval = len(review.ValidationFindings) == 0
	reference, matchReason, found, err := s.findReviewReference(candidate)
	if err != nil {
		return CandidateReview{}, err
	}
	if !found {
		return review, nil
	}
	review.HasReference = true
	review.Reference = reference
	review.MatchReason = matchReason
	review.ReferenceBodyLines = countTextLines(reference.Body)
	review.ChangedFields = buildReviewFieldDiffs(candidate, reference)
	review.BodyChanged = normalizeSkillBody(candidate.Body) != normalizeSkillBody(reference.Body)
	if review.BodyChanged {
		review.BodyDiffLines = buildBodyDiffLines(candidate.Body, reference.Body)
	}
	return review, nil
}

// approvedSkillsCache avoids re-reading skill files on every LoadApproved call.
// 3.8: Caches Definition by path with mtime check for invalidation.
var approvedSkillsCache sync.Map // map[string]cachedSkillDef

type cachedSkillDef struct {
	def       Definition
	cachedAt  time.Time
	fileMtime time.Time
}

func (s *Store) LoadApproved(approvedPath string) (Definition, error) {
	if s == nil {
		return Definition{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return Definition{}, err
	}

	resolvedPath, err := s.resolveApprovedPath(approvedPath)
	if err != nil {
		return Definition{}, err
	}

	// 3.8: Check cache before reading from disk.
	if cached, ok := approvedSkillsCache.Load(resolvedPath); ok {
		entry := cached.(cachedSkillDef)
		if info, statErr := os.Stat(resolvedPath); statErr == nil {
			if info.ModTime().Equal(entry.fileMtime) {
				return entry.def, nil
			}
		}
	}

	def, err := s.readDefinition(resolvedPath)
	if err != nil {
		return Definition{}, err
	}

	// Cache the result.
	if info, statErr := os.Stat(resolvedPath); statErr == nil {
		approvedSkillsCache.Store(resolvedPath, cachedSkillDef{
			def:       def,
			cachedAt:  time.Now(),
			fileMtime: info.ModTime(),
		})
	}

	return def, nil
}

func (s *Store) LoadInspect(path string) (Definition, error) {
	if s == nil {
		return Definition{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return Definition{}, err
	}
	resolvedPath, err := s.resolveGovernedCurrentPath(path)
	if err != nil {
		return Definition{}, err
	}
	return s.readDefinition(resolvedPath)
}

func ReadExternalDefinition(path string) (Definition, error) {
	store := &Store{}
	definition, err := store.readDefinition(filepath.Clean(path))
	if err != nil {
		return Definition{}, err
	}
	if definition.LifecycleState == "" {
		definition.LifecycleState = "external"
	}
	return definition, nil
}

func (s *Store) Status() ([]DirectoryStatus, error) {
	if s == nil {
		return nil, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return nil, err
	}
	directories := []DirectoryStatus{
		{Name: "approved", Path: s.approvedDir(), Purpose: "Activated skills that the runtime can list, load, and invoke.", Active: true},
		{Name: "generated", Path: s.generatedDir(), Purpose: "Candidate skills generated by runtime runs and waiting for explicit approval.", Active: true},
		{Name: "archive", Path: s.archiveDir(), Purpose: "Retired or disabled skills preserved for audit and rollback review; excluded from the runtime-loaded registry.", Active: true},
		// templates is reserved blueprint storage — not part of the runtime-loaded registry (S6.2/S6.8).
		{Name: "templates", Path: s.templatesDir(), Purpose: "Reusable skill blueprints and role templates for avatars.", Active: false},
	}
	for index := range directories {
		files, err := listMarkdownFiles(directories[index].Path)
		if err != nil {
			return nil, err
		}
		directories[index].Files = files
		directories[index].FileCount = len(files)
	}
	return directories, nil
}

func (s *Store) GovernanceSummary() (GovernanceSummary, error) {
	if s == nil {
		return GovernanceSummary{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceSummary{}, err
	}

	definitions, err := s.listGovernedDefinitions()
	if err != nil {
		return GovernanceSummary{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceSummary{}, err
	}
	summary := GovernanceSummary{TrackedSkills: len(definitions)}
	// PHANTOM-4: Count task_completed entries from ledger for success rate.
	for _, entry := range ledger.Entries {
		if entry.Action == "task_completed" {
			switch entry.ToState {
			case "success":
				summary.SuccessCount++
			case "failure":
				summary.FailureCount++
			}
		}
	}
	if total := summary.SuccessCount + summary.FailureCount; total > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(total)
	}
	transitions := make([]GovernanceTransition, 0)
	hotspots := make([]GovernanceHotspot, 0, len(definitions))
	alerts := make([]GovernanceAlert, 0)
	for _, definition := range definitions {
		switch definition.LifecycleState {
		case "generated":
			summary.GeneratedCount++
		case "approved":
			summary.ApprovedCount++
		case "archived":
			summary.ArchivedCount++
		case "disabled":
			summary.DisabledCount++
		}
		lastTransition, hasLastTransition := latestSkillHistoryEntry(definition.History)
		hotspots = append(hotspots, GovernanceHotspot{
			SkillName:        definition.Name,
			CurrentState:     definition.LifecycleState,
			Path:             definition.Path,
			TransitionCount:  len(definition.History),
			LastTransitionAt: lastTransition.At,
		})
		alerts = append(alerts, governanceSummaryAlerts(definition, ledger.Entries)...)
		for _, entry := range definition.History {
			transitions = append(transitions, GovernanceTransition{
				SkillName:         definition.Name,
				CurrentState:      definition.LifecycleState,
				SkillPath:         definition.Path,
				SkillHistoryEntry: entry,
			})
		}
		if !hasLastTransition {
			hotspots[len(hotspots)-1].LastTransitionAt = time.Time{}
		}
	}
	sort.Slice(hotspots, func(i int, j int) bool {
		if hotspots[i].TransitionCount != hotspots[j].TransitionCount {
			return hotspots[i].TransitionCount > hotspots[j].TransitionCount
		}
		if !hotspots[i].LastTransitionAt.Equal(hotspots[j].LastTransitionAt) {
			return hotspots[i].LastTransitionAt.After(hotspots[j].LastTransitionAt)
		}
		if hotspots[i].SkillName != hotspots[j].SkillName {
			return hotspots[i].SkillName < hotspots[j].SkillName
		}
		return hotspots[i].Path < hotspots[j].Path
	})
	sort.Slice(alerts, func(i int, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity < alerts[j].Severity
		}
		if alerts[i].SkillName != alerts[j].SkillName {
			return alerts[i].SkillName < alerts[j].SkillName
		}
		if alerts[i].Reason != alerts[j].Reason {
			return alerts[i].Reason < alerts[j].Reason
		}
		return alerts[i].Path < alerts[j].Path
	})
	sort.Slice(transitions, func(i int, j int) bool {
		if !transitions[i].At.Equal(transitions[j].At) {
			return transitions[i].At.After(transitions[j].At)
		}
		if transitions[i].SkillName != transitions[j].SkillName {
			return transitions[i].SkillName < transitions[j].SkillName
		}
		if transitions[i].Action != transitions[j].Action {
			return transitions[i].Action < transitions[j].Action
		}
		return transitions[i].Path < transitions[j].Path
	})
	summary.Hotspots = hotspots
	summary.Alerts = alerts
	summary.TransitionCount = len(transitions)
	summary.Transitions = transitions
	return summary, nil
}

func (s *Store) GovernanceLedger() (GovernanceLedger, error) {
	if s == nil {
		return GovernanceLedger{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceLedger{}, err
	}
	entries, err := s.readGovernanceLedgerEntries()
	if err != nil {
		return GovernanceLedger{}, err
	}
	sort.Slice(entries, func(i int, j int) bool {
		if !entries[i].At.Equal(entries[j].At) {
			return entries[i].At.After(entries[j].At)
		}
		if entries[i].SkillName != entries[j].SkillName {
			return entries[i].SkillName < entries[j].SkillName
		}
		if entries[i].Action != entries[j].Action {
			return entries[i].Action < entries[j].Action
		}
		return entries[i].SkillPath < entries[j].SkillPath
	})
	seen := make(map[string]struct{})
	for _, entry := range entries {
		seen[governanceLedgerSkillKey(entry)] = struct{}{}
	}
	return GovernanceLedger{EventCount: len(entries), SkillsSeen: len(seen), Entries: entries}, nil
}

func (s *Store) GovernanceReconciliation() (GovernanceReconciliation, error) {
	if s == nil {
		return GovernanceReconciliation{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceReconciliation{}, err
	}
	definitions, err := s.listGovernedDefinitions()
	if err != nil {
		return GovernanceReconciliation{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceReconciliation{}, err
	}

	currentByKey := make(map[string]GovernanceCurrentRecord, len(definitions))
	currentByPath := make(map[string]GovernanceCurrentRecord, len(definitions))
	currentDefinitionsByKey := make(map[string]Definition, len(definitions))
	currentDefinitionsByPath := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		key := governanceDefinitionKey(definition)
		currentDefinitionsByKey[key] = definition
		currentDefinitionsByPath[definition.Path] = definition
		record := GovernanceCurrentRecord{
			SkillName:     definition.Name,
			CurrentState:  definition.LifecycleState,
			Path:          definition.Path,
			ContentDigest: governanceDefinitionDigestValue(definition),
		}
		currentByKey[key] = record
		currentByPath[definition.Path] = record
	}

	latestLedgerByKey, latestKeys := latestGovernanceLedgerEntries(ledger.Entries)

	reconciliation := GovernanceReconciliation{
		CurrentSkills:    len(currentByKey),
		LedgerSkills:     len(latestLedgerByKey),
		MissingCurrent:   make([]GovernanceLedgerEntry, 0),
		UntrackedCurrent: make([]GovernanceCurrentRecord, 0),
		StateDrifts:      make([]GovernanceStateDrift, 0),
		PathDrifts:       make([]GovernancePathDrift, 0),
		ContentDrifts:    make([]GovernanceContentDrift, 0),
		MetadataDrifts:   make([]GovernanceMetadataDrift, 0),
		HistoryDrifts:    make([]GovernanceHistoryDrift, 0),
		InvariantDrifts:  make([]GovernanceInvariantDrift, 0),
	}

	matchedCurrentPaths := make(map[string]struct{})
	for _, key := range latestKeys {
		entry := latestLedgerByKey[key]
		current, ok := currentByKey[key]
		definition := currentDefinitionsByKey[key]
		if !ok {
			current, definition, ok = matchGovernedCurrentRecordByPath(entry.SkillPath, currentByPath, currentDefinitionsByPath)
		}
		if !ok {
			if isResolvedMissingState(entry.ToState) {
				continue
			}
			reconciliation.MissingCurrent = append(reconciliation.MissingCurrent, entry)
			continue
		}
		matchedCurrentPaths[current.Path] = struct{}{}
		if current.CurrentState != entry.ToState {
			reconciliation.StateDrifts = append(reconciliation.StateDrifts, GovernanceStateDrift{
				SkillName:    current.SkillName,
				CurrentState: current.CurrentState,
				LedgerState:  entry.ToState,
				CurrentPath:  current.Path,
				LedgerPath:   entry.SkillPath,
			})
		}
		if !sameGovernedPath(current.Path, entry.SkillPath) {
			reconciliation.PathDrifts = append(reconciliation.PathDrifts, GovernancePathDrift{
				SkillName:   current.SkillName,
				CurrentPath: current.Path,
				LedgerPath:  entry.SkillPath,
			})
		}
		if current.ContentDigest != entry.ContentDigest {
			reconciliation.ContentDrifts = append(reconciliation.ContentDrifts, GovernanceContentDrift{
				SkillName:     current.SkillName,
				CurrentPath:   current.Path,
				CurrentDigest: current.ContentDigest,
				LedgerDigest:  entry.ContentDigest,
				CurrentState:  current.CurrentState,
				LedgerState:   entry.ToState,
			})
		}
		expectedHistory := governanceExpectedHistory(entry, ledger.Entries)
		reconciliation.MetadataDrifts = append(reconciliation.MetadataDrifts, governanceMetadataDrifts(definition, entry, expectedHistory)...)
		if drift, ok := governanceHistoryDrift(definition, expectedHistory); ok {
			reconciliation.HistoryDrifts = append(reconciliation.HistoryDrifts, drift)
		}
		if definition.HistoryError == "" {
			invariantAlerts := governanceTransitionInvariantAlerts(definition.History)
			assessment := assessGovernanceInvariantRepair(definition, ledger.Entries)
			for _, reason := range invariantAlerts {
				reconciliation.InvariantDrifts = append(reconciliation.InvariantDrifts, GovernanceInvariantDrift{
					SkillName:         definition.Name,
					CurrentPath:       definition.Path,
					CurrentState:      definition.LifecycleState,
					Reason:            reason,
					Repairable:        assessment.Repairable,
					BlockedBy:         assessment.BlockedBy,
					SuggestedFollowUp: assessment.SuggestedFollowUp,
				})
			}
		}
	}

	for path, current := range currentByPath {
		if _, ok := matchedCurrentPaths[path]; ok {
			continue
		}
		reconciliation.UntrackedCurrent = append(reconciliation.UntrackedCurrent, current)
	}

	sort.Slice(reconciliation.UntrackedCurrent, func(i int, j int) bool {
		if reconciliation.UntrackedCurrent[i].SkillName != reconciliation.UntrackedCurrent[j].SkillName {
			return reconciliation.UntrackedCurrent[i].SkillName < reconciliation.UntrackedCurrent[j].SkillName
		}
		return reconciliation.UntrackedCurrent[i].Path < reconciliation.UntrackedCurrent[j].Path
	})
	sort.Slice(reconciliation.StateDrifts, func(i int, j int) bool {
		if reconciliation.StateDrifts[i].SkillName != reconciliation.StateDrifts[j].SkillName {
			return reconciliation.StateDrifts[i].SkillName < reconciliation.StateDrifts[j].SkillName
		}
		return reconciliation.StateDrifts[i].CurrentPath < reconciliation.StateDrifts[j].CurrentPath
	})
	sort.Slice(reconciliation.PathDrifts, func(i int, j int) bool {
		if reconciliation.PathDrifts[i].SkillName != reconciliation.PathDrifts[j].SkillName {
			return reconciliation.PathDrifts[i].SkillName < reconciliation.PathDrifts[j].SkillName
		}
		return reconciliation.PathDrifts[i].CurrentPath < reconciliation.PathDrifts[j].CurrentPath
	})
	sort.Slice(reconciliation.ContentDrifts, func(i int, j int) bool {
		if reconciliation.ContentDrifts[i].SkillName != reconciliation.ContentDrifts[j].SkillName {
			return reconciliation.ContentDrifts[i].SkillName < reconciliation.ContentDrifts[j].SkillName
		}
		return reconciliation.ContentDrifts[i].CurrentPath < reconciliation.ContentDrifts[j].CurrentPath
	})
	sort.Slice(reconciliation.MetadataDrifts, func(i int, j int) bool {
		if reconciliation.MetadataDrifts[i].SkillName != reconciliation.MetadataDrifts[j].SkillName {
			return reconciliation.MetadataDrifts[i].SkillName < reconciliation.MetadataDrifts[j].SkillName
		}
		if reconciliation.MetadataDrifts[i].CurrentPath != reconciliation.MetadataDrifts[j].CurrentPath {
			return reconciliation.MetadataDrifts[i].CurrentPath < reconciliation.MetadataDrifts[j].CurrentPath
		}
		return reconciliation.MetadataDrifts[i].Field < reconciliation.MetadataDrifts[j].Field
	})
	sort.Slice(reconciliation.HistoryDrifts, func(i int, j int) bool {
		if reconciliation.HistoryDrifts[i].SkillName != reconciliation.HistoryDrifts[j].SkillName {
			return reconciliation.HistoryDrifts[i].SkillName < reconciliation.HistoryDrifts[j].SkillName
		}
		return reconciliation.HistoryDrifts[i].CurrentPath < reconciliation.HistoryDrifts[j].CurrentPath
	})
	sort.Slice(reconciliation.InvariantDrifts, func(i int, j int) bool {
		if reconciliation.InvariantDrifts[i].SkillName != reconciliation.InvariantDrifts[j].SkillName {
			return reconciliation.InvariantDrifts[i].SkillName < reconciliation.InvariantDrifts[j].SkillName
		}
		if reconciliation.InvariantDrifts[i].CurrentPath != reconciliation.InvariantDrifts[j].CurrentPath {
			return reconciliation.InvariantDrifts[i].CurrentPath < reconciliation.InvariantDrifts[j].CurrentPath
		}
		return reconciliation.InvariantDrifts[i].Reason < reconciliation.InvariantDrifts[j].Reason
	})

	return reconciliation, nil
}

func (s *Store) ResolveMissingCurrent(target string) (GovernanceLedgerEntry, error) {
	if s == nil {
		return GovernanceLedgerEntry{}, errors.New("skills store is nil")
	}
	report, err := s.GovernanceReconciliation()
	if err != nil {
		return GovernanceLedgerEntry{}, err
	}
	entry, err := matchMissingCurrentEntry(report.MissingCurrent, target)
	if err != nil {
		return GovernanceLedgerEntry{}, err
	}
	resolved := GovernanceLedgerEntry{
		At:            time.Now().UTC(),
		Action:        "resolve-missing",
		FromState:     entry.ToState,
		ToState:       "missing",
		SkillName:     entry.SkillName,
		SkillPath:     entry.SkillPath,
		TemplateID:    entry.TemplateID,
		SourceRunID:   entry.SourceRunID,
		SourceTaskID:  entry.SourceTaskID,
		ContentDigest: entry.ContentDigest,
	}
	if err := s.appendGovernanceLedgerEntry(resolved); err != nil {
		return GovernanceLedgerEntry{}, fmt.Errorf("resolve missing current skill: %w", err)
	}
	return resolved, nil
}

func (s *Store) MissingCurrentRestoreGuide(target string) (GovernanceMissingRestoreGuide, error) {
	if s == nil {
		return GovernanceMissingRestoreGuide{}, errors.New("skills store is nil")
	}
	report, err := s.GovernanceReconciliation()
	if err != nil {
		return GovernanceMissingRestoreGuide{}, err
	}
	entry, err := matchMissingCurrentEntry(report.MissingCurrent, target)
	if err != nil {
		return GovernanceMissingRestoreGuide{}, err
	}
	return GovernanceMissingRestoreGuide{
		SkillName:               entry.SkillName,
		RecordedPath:            entry.SkillPath,
		LatestAction:            entry.Action,
		LatestLifecycleState:    entry.ToState,
		SourceRunID:             entry.SourceRunID,
		SourceTaskID:            entry.SourceTaskID,
		TemplateID:              entry.TemplateID,
		SuggestedReconcileCheck: "avatars skills reconcile",
	}, nil
}

func (s *Store) RestoreMissingCurrent(target string, sourcePath string) (GovernanceMissingRestoreResult, error) {
	if s == nil {
		return GovernanceMissingRestoreResult{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceMissingRestoreResult{}, err
	}
	guide, err := s.MissingCurrentRestoreGuide(target)
	if err != nil {
		return GovernanceMissingRestoreResult{}, err
	}
	resolvedSourcePath, err := resolveExternalSkillSourcePath(sourcePath)
	if err != nil {
		return GovernanceMissingRestoreResult{}, err
	}
	if _, err := os.Stat(guide.RecordedPath); err == nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("recorded restore path %q already exists", guide.RecordedPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("stat recorded restore path %s: %w", guide.RecordedPath, err)
	}
	sourceContent, err := os.ReadFile(resolvedSourcePath)
	if err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("read restore source %s: %w", resolvedSourcePath, err)
	}
	restoredContent, err := rewriteFrontmatterScalars(string(sourceContent), map[string]string{
		"lifecycle-state": guide.LatestLifecycleState,
		"source-run-id":   guide.SourceRunID,
		"source-task-id":  guide.SourceTaskID,
		"template-id":     guide.TemplateID,
	})
	if err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("prepare restored missing skill: %w", err)
	}
	definition, err := loadDefinitionFromContent(guide.RecordedPath, restoredContent)
	if err != nil {
		return GovernanceMissingRestoreResult{}, err
	}
	if definition.Name != guide.SkillName {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("restore source skill name %q does not match missing skill %q", definition.Name, guide.SkillName)
	}
	if guide.TemplateID != "" && definition.TemplateID != guide.TemplateID {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("restore source template id %q does not match missing skill template id %q", definition.TemplateID, guide.TemplateID)
	}
	if err := os.MkdirAll(filepath.Dir(guide.RecordedPath), 0o755); err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("create restore dir %s: %w", filepath.Dir(guide.RecordedPath), err)
	}
	if err := os.WriteFile(guide.RecordedPath, []byte(restoredContent), 0o644); err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("write restored missing skill %s: %w", guide.RecordedPath, err)
	}
	restoredAt := time.Now().UTC()
	if err := writeSkillHistory(guide.RecordedPath, []SkillHistoryEntry{{At: restoredAt, Action: "restore-missing-current", ToState: guide.LatestLifecycleState, Path: guide.RecordedPath}}); err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("write restored missing skill history: %w", err)
	}
	restoredDefinition, err := s.readDefinition(guide.RecordedPath)
	if err != nil {
		return GovernanceMissingRestoreResult{}, err
	}
	entry := governanceLedgerEntryFromDefinition(restoredDefinition, "restore-missing-current", guide.LatestLifecycleState, guide.LatestLifecycleState, guide.RecordedPath, restoredAt)
	if err := s.appendGovernanceLedgerEntry(entry); err != nil {
		return GovernanceMissingRestoreResult{}, fmt.Errorf("record restored missing skill ledger entry: %w", err)
	}
	return GovernanceMissingRestoreResult{
		RestoredPath:            guide.RecordedPath,
		SourcePath:              resolvedSourcePath,
		LifecycleState:          guide.LatestLifecycleState,
		LedgerEntry:             entry,
		SuggestedReconcileCheck: guide.SuggestedReconcileCheck,
	}, nil
}

func (s *Store) RepairMetadata(target string) (GovernanceMetadataRepairResult, error) {
	if s == nil {
		return GovernanceMetadataRepairResult{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceMetadataRepairResult{}, err
	}
	resolvedPath, err := s.resolveGovernedCurrentPath(target)
	if err != nil {
		return GovernanceMetadataRepairResult{}, err
	}
	definition, err := s.readDefinition(resolvedPath)
	if err != nil {
		return GovernanceMetadataRepairResult{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceMetadataRepairResult{}, err
	}
	latest, ok := resolveLatestGovernanceEntryForDefinition(definition, resolvedPath, ledger.Entries)
	if !ok {
		return GovernanceMetadataRepairResult{}, fmt.Errorf("no governance ledger entry found for %q", resolvedPath)
	}
	updates := governanceMetadataRepairUpdates(definition, latest)
	if len(updates) == 0 {
		return GovernanceMetadataRepairResult{}, errors.New("no repairable metadata drift found for skill")
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return GovernanceMetadataRepairResult{}, fmt.Errorf("read skill file %s: %w", resolvedPath, err)
	}
	repairedContent, err := rewriteFrontmatterScalars(string(content), updates)
	if err != nil {
		return GovernanceMetadataRepairResult{}, fmt.Errorf("repair metadata for %s: %w", resolvedPath, err)
	}
	if err := os.WriteFile(resolvedPath, []byte(repairedContent), 0o644); err != nil {
		return GovernanceMetadataRepairResult{}, fmt.Errorf("write repaired skill file %s: %w", resolvedPath, err)
	}
	repairedDefinition, err := s.readDefinition(resolvedPath)
	if err != nil {
		return GovernanceMetadataRepairResult{}, err
	}
	repairedAt := time.Now().UTC()
	entry := governanceLedgerEntryFromDefinition(repairedDefinition, "repair-current-metadata", repairedDefinition.LifecycleState, repairedDefinition.LifecycleState, resolvedPath, repairedAt)
	if err := s.appendGovernanceLedgerEntry(entry); err != nil {
		return GovernanceMetadataRepairResult{}, fmt.Errorf("record metadata repair ledger entry: %w", err)
	}
	updatedFields := make([]string, 0, len(updates))
	for field := range updates {
		updatedFields = append(updatedFields, field)
	}
	sort.Strings(updatedFields)
	return GovernanceMetadataRepairResult{
		RepairedPath:            resolvedPath,
		LifecycleState:          repairedDefinition.LifecycleState,
		UpdatedFields:           updatedFields,
		LedgerEntry:             entry,
		SuggestedReconcileCheck: "avatars skills reconcile",
	}, nil
}

func (s *Store) RepairHistory(target string) (GovernanceHistoryRepairResult, error) {
	if s == nil {
		return GovernanceHistoryRepairResult{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceHistoryRepairResult{}, err
	}
	resolvedPath, err := s.resolveGovernedCurrentPath(target)
	if err != nil {
		return GovernanceHistoryRepairResult{}, err
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return GovernanceHistoryRepairResult{}, fmt.Errorf("read skill file %s: %w", resolvedPath, err)
	}
	definition, err := loadDefinitionFromContent(resolvedPath, string(content))
	if err != nil {
		return GovernanceHistoryRepairResult{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceHistoryRepairResult{}, err
	}
	latest, ok := resolveLatestGovernanceEntryForDefinition(definition, resolvedPath, ledger.Entries)
	if !ok {
		return GovernanceHistoryRepairResult{}, fmt.Errorf("no governance ledger entry found for %q", resolvedPath)
	}
	expectedHistory := governanceExpectedHistory(latest, ledger.Entries)
	if len(expectedHistory) == 0 {
		return GovernanceHistoryRepairResult{}, errors.New("no repairable history drift found for skill")
	}
	currentHistory, historyErr := readSkillHistory(resolvedPath)
	if historyErr == nil && skillHistoryEqual(currentHistory, expectedHistory) {
		return GovernanceHistoryRepairResult{}, errors.New("no repairable history drift found for skill")
	}
	if err := writeSkillHistory(resolvedPath, expectedHistory); err != nil {
		return GovernanceHistoryRepairResult{}, fmt.Errorf("write repaired skill history for %s: %w", resolvedPath, err)
	}
	repairedAt := time.Now().UTC()
	entry := governanceLedgerEntryFromDefinition(definition, "repair-current-history", definition.LifecycleState, definition.LifecycleState, resolvedPath, repairedAt)
	if err := s.appendGovernanceLedgerEntry(entry); err != nil {
		return GovernanceHistoryRepairResult{}, fmt.Errorf("record history repair ledger entry: %w", err)
	}
	return GovernanceHistoryRepairResult{
		RepairedPath:            resolvedPath,
		LifecycleState:          definition.LifecycleState,
		RebuiltTransitions:      len(expectedHistory),
		LedgerEntry:             entry,
		SuggestedReconcileCheck: "avatars skills reconcile",
	}, nil
}

func (s *Store) RepairInvariants(target string) (GovernanceInvariantRepairResult, error) {
	if s == nil {
		return GovernanceInvariantRepairResult{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceInvariantRepairResult{}, err
	}
	resolvedPath, err := s.resolveGovernedCurrentPath(target)
	if err != nil {
		return GovernanceInvariantRepairResult{}, err
	}
	definition, err := s.readDefinition(resolvedPath)
	if err != nil {
		return GovernanceInvariantRepairResult{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceInvariantRepairResult{}, err
	}
	assessment := assessGovernanceInvariantRepair(definition, ledger.Entries)
	if !assessment.Repairable {
		if assessment.BlockedBy != "" {
			return GovernanceInvariantRepairResult{}, fmt.Errorf("lifecycle invariant drift is not repairable from current governance ledger: %s", assessment.BlockedBy)
		}
		return GovernanceInvariantRepairResult{}, errors.New("no repairable lifecycle invariant drift found for skill")
	}
	if err := writeSkillHistory(resolvedPath, assessment.ExpectedHistory); err != nil {
		return GovernanceInvariantRepairResult{}, fmt.Errorf("write repaired invariant history for %s: %w", resolvedPath, err)
	}
	repairedAt := time.Now().UTC()
	entry := governanceLedgerEntryFromDefinition(definition, "repair-current-invariants", definition.LifecycleState, definition.LifecycleState, resolvedPath, repairedAt)
	if err := s.appendGovernanceLedgerEntry(entry); err != nil {
		return GovernanceInvariantRepairResult{}, fmt.Errorf("record invariant repair ledger entry: %w", err)
	}
	return GovernanceInvariantRepairResult{
		RepairedPath:            resolvedPath,
		LifecycleState:          definition.LifecycleState,
		RebuiltTransitions:      len(assessment.ExpectedHistory),
		LedgerEntry:             entry,
		SuggestedReconcileCheck: "avatars skills reconcile",
	}, nil
}

func governanceAlertTargetsInvariant(reason string) bool {
	return strings.HasPrefix(reason, "broken-transition-chain:") ||
		strings.HasPrefix(reason, "invalid-transition:") ||
		strings.HasPrefix(reason, "invalid-start-transition:")
}

func assessGovernanceInvariantRepair(definition Definition, ledgerEntries []GovernanceLedgerEntry) governanceInvariantRepairAssessment {
	assessment := governanceInvariantRepairAssessment{SuggestedFollowUp: "avatars skills reconcile"}
	if definition.HistoryError != "" || len(definition.History) == 0 {
		assessment.BlockedBy = "current-history-unavailable"
		return assessment
	}
	if len(governanceTransitionInvariantAlerts(definition.History)) == 0 {
		assessment.BlockedBy = "no-current-invariant-drift"
		return assessment
	}
	latest, ok := resolveLatestGovernanceEntryForDefinition(definition, definition.Path, ledgerEntries)
	if !ok {
		assessment.BlockedBy = "missing-ledger-entry"
		assessment.SuggestedFollowUp = "avatars skills ledger"
		return assessment
	}
	expectedHistory := governanceExpectedHistory(latest, ledgerEntries)
	if len(expectedHistory) == 0 {
		assessment.BlockedBy = "missing-ledger-history"
		assessment.SuggestedFollowUp = "avatars skills ledger"
		return assessment
	}
	if latestExpected, ok := latestSkillHistoryEntry(expectedHistory); ok && latestExpected.ToState != "" && latestExpected.ToState != definition.LifecycleState {
		assessment.BlockedBy = fmt.Sprintf("ledger-latest-state-mismatch: latest=%s current=%s", latestExpected.ToState, definition.LifecycleState)
		assessment.SuggestedFollowUp = "avatars skills reconcile"
		return assessment
	}
	if ledgerInvariantAlerts := governanceTransitionInvariantAlerts(expectedHistory); len(ledgerInvariantAlerts) > 0 {
		assessment.BlockedBy = "ledger-" + ledgerInvariantAlerts[0]
		assessment.SuggestedFollowUp = "avatars skills ledger"
		return assessment
	}
	if skillHistoryEqual(definition.History, expectedHistory) {
		assessment.BlockedBy = "current-history-aligned-with-ledger"
		return assessment
	}
	target := filepath.Base(strings.TrimSpace(definition.Path))
	if target != "" && target != "." {
		assessment.SuggestedFollowUp = fmt.Sprintf("avatars skills repair-invariants %s", target)
	}
	assessment.Repairable = true
	assessment.ExpectedHistory = expectedHistory
	assessment.BlockedBy = ""
	return assessment
}

func (s *Store) SyncGovernanceLedger() (GovernanceLedgerSync, error) {
	if s == nil {
		return GovernanceLedgerSync{}, errors.New("skills store is nil")
	}
	if err := s.ensureLayout(); err != nil {
		return GovernanceLedgerSync{}, err
	}
	definitions, err := s.listGovernedDefinitions()
	if err != nil {
		return GovernanceLedgerSync{}, err
	}
	ledger, err := s.GovernanceLedger()
	if err != nil {
		return GovernanceLedgerSync{}, err
	}

	latestLedgerByKey, latestKeys := latestGovernanceLedgerEntries(ledger.Entries)
	sort.Slice(definitions, func(i int, j int) bool {
		leftKey := governanceDefinitionKey(definitions[i])
		rightKey := governanceDefinitionKey(definitions[j])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return definitions[i].Path < definitions[j].Path
	})

	syncReport := GovernanceLedgerSync{
		AdoptedCurrent:    make([]GovernanceLedgerEntry, 0),
		SyncedCurrent:     make([]GovernanceLedgerSyncEntry, 0),
		UnresolvedMissing: make([]GovernanceLedgerEntry, 0),
	}
	currentKeys := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		key := governanceDefinitionKey(definition)
		currentKeys[key] = struct{}{}
		latest, tracked := latestLedgerByKey[key]
		recordedAt := time.Now().UTC()
		if !tracked {
			entry := governanceLedgerEntryFromDefinition(definition, "adopt-current", "", definition.LifecycleState, definition.Path, recordedAt)
			if err := s.appendGovernanceLedgerEntry(entry); err != nil {
				return GovernanceLedgerSync{}, fmt.Errorf("adopt current skill into governance ledger: %w", err)
			}
			syncReport.AdoptedCurrent = append(syncReport.AdoptedCurrent, entry)
			latestLedgerByKey[key] = entry
			continue
		}

		stateChanged := definition.LifecycleState != latest.ToState
		pathChanged := definition.Path != latest.SkillPath
		contentChanged := governanceDefinitionDigestValue(definition) != latest.ContentDigest
		if !stateChanged && !pathChanged && !contentChanged {
			continue
		}

		entry := governanceLedgerEntryFromDefinition(definition, governanceSyncAction(stateChanged, pathChanged, contentChanged), latest.ToState, definition.LifecycleState, definition.Path, recordedAt)
		if err := s.appendGovernanceLedgerEntry(entry); err != nil {
			return GovernanceLedgerSync{}, fmt.Errorf("sync current skill into governance ledger: %w", err)
		}
		syncReport.SyncedCurrent = append(syncReport.SyncedCurrent, GovernanceLedgerSyncEntry{
			Entry:          entry,
			PreviousPath:   latest.SkillPath,
			StateChanged:   stateChanged,
			PathChanged:    pathChanged,
			ContentChanged: contentChanged,
		})
		latestLedgerByKey[key] = entry
	}

	for _, key := range latestKeys {
		if _, ok := currentKeys[key]; ok {
			continue
		}
		if isResolvedMissingState(latestLedgerByKey[key].ToState) {
			continue
		}
		syncReport.UnresolvedMissing = append(syncReport.UnresolvedMissing, latestLedgerByKey[key])
	}

	sort.Slice(syncReport.AdoptedCurrent, func(i int, j int) bool {
		if syncReport.AdoptedCurrent[i].SkillName != syncReport.AdoptedCurrent[j].SkillName {
			return syncReport.AdoptedCurrent[i].SkillName < syncReport.AdoptedCurrent[j].SkillName
		}
		return syncReport.AdoptedCurrent[i].SkillPath < syncReport.AdoptedCurrent[j].SkillPath
	})
	sort.Slice(syncReport.SyncedCurrent, func(i int, j int) bool {
		if syncReport.SyncedCurrent[i].Entry.SkillName != syncReport.SyncedCurrent[j].Entry.SkillName {
			return syncReport.SyncedCurrent[i].Entry.SkillName < syncReport.SyncedCurrent[j].Entry.SkillName
		}
		return syncReport.SyncedCurrent[i].Entry.SkillPath < syncReport.SyncedCurrent[j].Entry.SkillPath
	})

	return syncReport, nil
}

func latestSkillHistoryEntry(history []SkillHistoryEntry) (SkillHistoryEntry, bool) {
	if len(history) == 0 {
		return SkillHistoryEntry{}, false
	}
	latest := history[0]
	for _, entry := range history[1:] {
		if entry.At.After(latest.At) {
			latest = entry
		}
	}
	return latest, true
}

func governanceLedgerEntryFromDefinition(definition Definition, action string, fromState string, toState string, skillPath string, at time.Time) GovernanceLedgerEntry {
	return GovernanceLedgerEntry{
		At:            at,
		Action:        action,
		FromState:     fromState,
		ToState:       toState,
		SkillName:     definition.Name,
		SkillPath:     skillPath,
		TemplateID:    definition.TemplateID,
		SourceRunID:   definition.SourceRunID,
		SourceTaskID:  definition.SourceTaskID,
		ContentDigest: governanceDefinitionDigestValue(definition),
	}
}

func latestGovernanceLedgerEntries(entries []GovernanceLedgerEntry) (map[string]GovernanceLedgerEntry, []string) {
	latestByKey := make(map[string]GovernanceLedgerEntry)
	orderedKeys := make([]string, 0)
	for _, entry := range entries {
		key := governanceLedgerSkillKey(entry)
		if _, ok := latestByKey[key]; ok {
			continue
		}
		latestByKey[key] = entry
		orderedKeys = append(orderedKeys, key)
	}
	return latestByKey, orderedKeys
}

func governanceSyncAction(stateChanged bool, pathChanged bool, contentChanged bool) string {
	parts := []string{"sync-current"}
	if stateChanged {
		parts = append(parts, "state")
	}
	if pathChanged {
		parts = append(parts, "path")
	}
	if contentChanged {
		parts = append(parts, "content")
	}
	return strings.Join(parts, "-")
}

func governanceDefinitionDigest(definition Definition) (string, error) {
	allowedTools := append([]string(nil), definition.AllowedTools...)
	sort.Strings(allowedTools)
	payload := struct {
		Name                   string   `json:"name"`
		Description            string   `json:"description"`
		WhenToUse              string   `json:"when_to_use"`
		AllowedTools           []string `json:"allowed_tools"`
		Context                string   `json:"context"`
		Version                string   `json:"version"`
		TemplateID             string   `json:"template_id,omitempty"`
		UserInvocable          bool     `json:"user_invocable"`
		DisableModelInvocation bool     `json:"disable_model_invocation"`
		Body                   string   `json:"body"`
	}{
		Name:                   definition.Name,
		Description:            definition.Description,
		WhenToUse:              definition.WhenToUse,
		AllowedTools:           allowedTools,
		Context:                definition.Context,
		Version:                definition.Version,
		TemplateID:             definition.TemplateID,
		UserInvocable:          definition.UserInvocable,
		DisableModelInvocation: definition.DisableModelInvocation,
		Body:                   strings.TrimSpace(definition.Body),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func governanceDefinitionDigestValue(definition Definition) string {
	digest, err := governanceDefinitionDigest(definition)
	if err != nil {
		sum := sha256.Sum256([]byte("digest-error:" + definition.Name + ":" + definition.Path))
		return hex.EncodeToString(sum[:])
	}
	return digest
}

func resolveLatestGovernanceEntryForDefinition(definition Definition, currentPath string, ledgerEntries []GovernanceLedgerEntry) (GovernanceLedgerEntry, bool) {
	latestLedgerByKey, _ := latestGovernanceLedgerEntries(ledgerEntries)
	latest, ok := latestLedgerByKey[governanceDefinitionKey(definition)]
	if !ok {
		latest, ok = matchGovernedCurrentEntryByPath(currentPath, latestLedgerByKey)
	}
	return latest, ok
}

func governanceExpectedHistory(latest GovernanceLedgerEntry, ledgerEntries []GovernanceLedgerEntry) []SkillHistoryEntry {
	canonicalKey := governanceLedgerSkillKey(latest)
	history := make([]SkillHistoryEntry, 0)
	for _, entry := range ledgerEntries {
		if governanceLedgerSkillKey(entry) != canonicalKey {
			continue
		}
		historyEntry, ok := governanceHistoryEntryFromLedger(entry)
		if !ok {
			continue
		}
		history = append(history, historyEntry)
	}
	sort.Slice(history, func(i int, j int) bool {
		if history[i].At.Equal(history[j].At) {
			if history[i].Action != history[j].Action {
				return history[i].Action < history[j].Action
			}
			return history[i].Path < history[j].Path
		}
		return history[i].At.Before(history[j].At)
	})
	return history
}

func governanceHistoryEntryFromLedger(entry GovernanceLedgerEntry) (SkillHistoryEntry, bool) {
	switch entry.Action {
	case "generate", "approve", "archive", "disable", "restore", "restore-missing-current":
		return SkillHistoryEntry{At: entry.At, Action: entry.Action, FromState: entry.FromState, ToState: entry.ToState, Path: entry.SkillPath}, true
	default:
		return SkillHistoryEntry{}, false
	}
}

func governanceHistoryDrift(definition Definition, expected []SkillHistoryEntry) (GovernanceHistoryDrift, bool) {
	actual := definition.History
	if definition.HistoryError != "" {
		latestExpectedAction := "unknown"
		if len(expected) > 0 {
			latestExpectedAction = expected[len(expected)-1].Action
		}
		return GovernanceHistoryDrift{
			SkillName:            definition.Name,
			CurrentPath:          definition.Path,
			CurrentState:         definition.LifecycleState,
			Reason:               "corrupted-history",
			ExpectedTransitions:  len(expected),
			ActualTransitions:    0,
			ExpectedLatestAction: latestExpectedAction,
			ActualLatestAction:   "invalid",
		}, true
	}
	if len(expected) == 0 {
		if len(actual) == 0 {
			return GovernanceHistoryDrift{}, false
		}
		latestActualAction := "unknown"
		if latestActual, ok := latestSkillHistoryEntry(actual); ok {
			latestActualAction = latestActual.Action
		}
		return GovernanceHistoryDrift{
			SkillName:            definition.Name,
			CurrentPath:          definition.Path,
			CurrentState:         definition.LifecycleState,
			Reason:               "unexpected-history",
			ExpectedTransitions:  0,
			ActualTransitions:    len(actual),
			ExpectedLatestAction: "unknown",
			ActualLatestAction:   latestActualAction,
		}, true
	}
	if skillHistoryEqual(actual, expected) {
		return GovernanceHistoryDrift{}, false
	}
	reason := "history-mismatch"
	if len(actual) == 0 {
		reason = "missing-history"
	} else if len(actual) != len(expected) {
		reason = "transition-count-mismatch"
	}
	latestExpectedAction := expected[len(expected)-1].Action
	latestActualAction := "unknown"
	if latestActual, ok := latestSkillHistoryEntry(actual); ok {
		latestActualAction = latestActual.Action
	}
	return GovernanceHistoryDrift{
		SkillName:            definition.Name,
		CurrentPath:          definition.Path,
		CurrentState:         definition.LifecycleState,
		Reason:               reason,
		ExpectedTransitions:  len(expected),
		ActualTransitions:    len(actual),
		ExpectedLatestAction: latestExpectedAction,
		ActualLatestAction:   latestActualAction,
	}, true
}

func skillHistoryEqual(left []SkillHistoryEntry, right []SkillHistoryEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !left[i].At.Equal(right[i].At) || left[i].Action != right[i].Action || left[i].FromState != right[i].FromState || left[i].ToState != right[i].ToState || left[i].Path != right[i].Path {
			return false
		}
	}
	return true
}

func governanceTransitionInvariantAlerts(history []SkillHistoryEntry) []string {
	alerts := make([]string, 0)
	for index, entry := range history {
		if index == 0 {
			if entry.Action != "generate" && entry.Action != "restore-missing-current" {
				alerts = append(alerts, fmt.Sprintf("invalid-start-transition: %s %s -> %s", entry.Action, displayInvariantState(entry.FromState), displayInvariantState(entry.ToState)))
			}
		} else {
			previous := history[index-1]
			if entry.FromState != previous.ToState {
				alerts = append(alerts, fmt.Sprintf("broken-transition-chain: expected-from=%s actual-from=%s action=%s", displayInvariantState(previous.ToState), displayInvariantState(entry.FromState), entry.Action))
			}
		}
		if !isAllowedLifecycleTransition(entry) {
			alerts = append(alerts, fmt.Sprintf("invalid-transition: %s %s -> %s", entry.Action, displayInvariantState(entry.FromState), displayInvariantState(entry.ToState)))
		}
	}
	return dedupeStrings(alerts)
}

func governanceAlertsForDefinition(definition Definition) []GovernanceAlert {
	alerts := make([]GovernanceAlert, 0)
	if definition.HistoryError != "" {
		alerts = append(alerts, GovernanceAlert{Severity: "error", SkillName: definition.Name, Reason: "corrupted-history", Path: definition.Path})
		return alerts
	}
	if len(definition.History) == 0 {
		if strings.TrimSpace(definition.SourceRunID) == "" && strings.TrimSpace(definition.SourceTaskID) == "" {
			return alerts
		}
		alerts = append(alerts, GovernanceAlert{Severity: "warning", SkillName: definition.Name, Reason: "missing-history", Path: definition.Path})
		return alerts
	}
	if lastTransition, ok := latestSkillHistoryEntry(definition.History); ok && lastTransition.ToState != "" && lastTransition.ToState != definition.LifecycleState {
		alerts = append(alerts, GovernanceAlert{Severity: "error", SkillName: definition.Name, Reason: fmt.Sprintf("state-mismatch: latest=%s current=%s", lastTransition.ToState, definition.LifecycleState), Path: definition.Path})
	}
	for _, reason := range governanceTransitionInvariantAlerts(definition.History) {
		alerts = append(alerts, GovernanceAlert{Severity: "error", SkillName: definition.Name, Reason: reason, Path: definition.Path})
	}
	if len(definition.History) >= 4 {
		alerts = append(alerts, GovernanceAlert{Severity: "warning", SkillName: definition.Name, Reason: fmt.Sprintf("high-churn: %d transitions", len(definition.History)), Path: definition.Path})
	}
	return alerts
}

func governanceSummaryAlerts(definition Definition, ledgerEntries []GovernanceLedgerEntry) []GovernanceAlert {
	alerts := make([]GovernanceAlert, 0, len(definition.GovernanceAlerts))
	latest, found := resolveLatestGovernanceEntryForDefinition(definition, definition.Path, ledgerEntries)
	expectedHistory := []SkillHistoryEntry(nil)
	if found {
		expectedHistory = governanceExpectedHistory(latest, ledgerEntries)
	}
	for _, alert := range definition.GovernanceAlerts {
		if alert.Reason == "missing-history" && len(expectedHistory) == 0 {
			continue
		}
		alerts = append(alerts, alert)
	}
	return alerts
}

func isAllowedLifecycleTransition(entry SkillHistoryEntry) bool {
	switch entry.Action {
	case "generate":
		return entry.FromState == "" && entry.ToState == "generated"
	case "approve":
		return entry.FromState == "generated" && entry.ToState == "approved"
	case "archive":
		return entry.FromState == "approved" && entry.ToState == "archived"
	case "disable":
		return entry.FromState == "approved" && entry.ToState == "disabled"
	case "restore":
		return (entry.FromState == "archived" || entry.FromState == "disabled") && entry.ToState == "approved"
	case "restore-missing-current":
		return entry.FromState == "" && (entry.ToState == "generated" || entry.ToState == "approved" || entry.ToState == "archived" || entry.ToState == "disabled")
	default:
		return false
	}
}

func displayInvariantState(state string) string {
	if strings.TrimSpace(state) == "" {
		return "unknown"
	}
	return state
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func matchGovernedCurrentEntryByPath(currentPath string, latestByKey map[string]GovernanceLedgerEntry) (GovernanceLedgerEntry, bool) {
	for _, entry := range latestByKey {
		if sameGovernedPath(entry.SkillPath, currentPath) {
			return entry, true
		}
	}
	return GovernanceLedgerEntry{}, false
}

func matchGovernedCurrentRecordByPath(recordedPath string, currentByPath map[string]GovernanceCurrentRecord, definitionsByPath map[string]Definition) (GovernanceCurrentRecord, Definition, bool) {
	for currentPath, record := range currentByPath {
		if sameGovernedPath(currentPath, recordedPath) {
			return record, definitionsByPath[currentPath], true
		}
	}
	return GovernanceCurrentRecord{}, Definition{}, false
}

func sameGovernedPath(left string, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil {
		return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
	}
	return false
}

func governanceMetadataDrifts(definition Definition, latest GovernanceLedgerEntry, expectedHistory []SkillHistoryEntry) []GovernanceMetadataDrift {
	drifts := make([]GovernanceMetadataDrift, 0)
	appendDrift := func(field string, expected string, actual string) {
		drifts = append(drifts, GovernanceMetadataDrift{
			SkillName:    definition.Name,
			CurrentPath:  definition.Path,
			CurrentState: definition.LifecycleState,
			Field:        field,
			Expected:     expected,
			Actual:       actual,
		})
	}
	if strings.TrimSpace(latest.SourceRunID) != "" && definition.SourceRunID != latest.SourceRunID {
		appendDrift("SourceRunID", latest.SourceRunID, displayMetadataValue(definition.SourceRunID))
	}
	if strings.TrimSpace(latest.SourceTaskID) != "" && definition.SourceTaskID != latest.SourceTaskID {
		appendDrift("SourceTaskID", latest.SourceTaskID, displayMetadataValue(definition.SourceTaskID))
	}
	switch definition.LifecycleState {
	case "generated":
		if definition.GeneratedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "generated") {
			appendDrift("GeneratedAt", "present", "unknown")
		}
		if !definition.ApprovedAt.IsZero() {
			appendDrift("ApprovedAt", "unknown", definition.ApprovedAt.UTC().Format(time.RFC3339))
		}
		if !definition.ArchivedAt.IsZero() {
			appendDrift("ArchivedAt", "unknown", definition.ArchivedAt.UTC().Format(time.RFC3339))
		}
		if !definition.DisabledAt.IsZero() {
			appendDrift("DisabledAt", "unknown", definition.DisabledAt.UTC().Format(time.RFC3339))
		}
	case "approved":
		if definition.GeneratedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "generated") {
			appendDrift("GeneratedAt", "present", "unknown")
		}
		if definition.ApprovedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "approved") {
			appendDrift("ApprovedAt", "present", "unknown")
		}
		if !definition.ArchivedAt.IsZero() {
			appendDrift("ArchivedAt", "unknown", definition.ArchivedAt.UTC().Format(time.RFC3339))
		}
		if !definition.DisabledAt.IsZero() {
			appendDrift("DisabledAt", "unknown", definition.DisabledAt.UTC().Format(time.RFC3339))
		}
	case "archived":
		if definition.GeneratedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "generated") {
			appendDrift("GeneratedAt", "present", "unknown")
		}
		if definition.ApprovedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "approved") {
			appendDrift("ApprovedAt", "present", "unknown")
		}
		if definition.ArchivedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "archived") {
			appendDrift("ArchivedAt", "present", "unknown")
		}
		if !definition.DisabledAt.IsZero() {
			appendDrift("DisabledAt", "unknown", definition.DisabledAt.UTC().Format(time.RFC3339))
		}
	case "disabled":
		if definition.GeneratedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "generated") {
			appendDrift("GeneratedAt", "present", "unknown")
		}
		if definition.ApprovedAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "approved") {
			appendDrift("ApprovedAt", "present", "unknown")
		}
		if definition.DisabledAt.IsZero() && hasExpectedLifecycleTransition(expectedHistory, "disabled") {
			appendDrift("DisabledAt", "present", "unknown")
		}
		if !definition.ArchivedAt.IsZero() {
			appendDrift("ArchivedAt", "unknown", definition.ArchivedAt.UTC().Format(time.RFC3339))
		}
	}
	return drifts
}

func hasExpectedLifecycleTransition(history []SkillHistoryEntry, toState string) bool {
	for _, entry := range history {
		if entry.ToState == toState {
			return true
		}
	}
	return false
}

func displayMetadataValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func governanceMetadataRepairUpdates(definition Definition, latest GovernanceLedgerEntry) map[string]string {
	updates := map[string]string{}
	if latest.SourceRunID != "" && definition.SourceRunID != latest.SourceRunID {
		updates["source-run-id"] = latest.SourceRunID
	}
	if latest.SourceTaskID != "" && definition.SourceTaskID != latest.SourceTaskID {
		updates["source-task-id"] = latest.SourceTaskID
	}
	if latest.TemplateID != "" && definition.TemplateID != latest.TemplateID {
		updates["template-id"] = latest.TemplateID
	}
	if generatedAt, ok := lifecycleTransitionTime(definition.History, "generated", false); ok {
		if definition.GeneratedAt.IsZero() || !definition.GeneratedAt.Equal(generatedAt) {
			updates["generated-at"] = generatedAt.UTC().Format(time.RFC3339)
		}
	}
	if approvedAt, ok := lifecycleTransitionTime(definition.History, "approved", false); ok {
		if definition.LifecycleState == "approved" || definition.LifecycleState == "archived" || definition.LifecycleState == "disabled" {
			if definition.ApprovedAt.IsZero() || !definition.ApprovedAt.Equal(approvedAt) {
				updates["approved-at"] = approvedAt.UTC().Format(time.RFC3339)
			}
		}
	}
	switch definition.LifecycleState {
	case "generated":
		if !definition.ApprovedAt.IsZero() {
			updates["approved-at"] = ""
		}
		if !definition.ArchivedAt.IsZero() {
			updates["archived-at"] = ""
		}
		if !definition.DisabledAt.IsZero() {
			updates["disabled-at"] = ""
		}
	case "approved":
		if !definition.ArchivedAt.IsZero() {
			updates["archived-at"] = ""
		}
		if !definition.DisabledAt.IsZero() {
			updates["disabled-at"] = ""
		}
	case "archived":
		if archivedAt, ok := lifecycleTransitionTime(definition.History, "archived", true); ok {
			if definition.ArchivedAt.IsZero() || !definition.ArchivedAt.Equal(archivedAt) {
				updates["archived-at"] = archivedAt.UTC().Format(time.RFC3339)
			}
		} else if definition.ArchivedAt.IsZero() && latest.ToState == "archived" {
			updates["archived-at"] = latest.At.UTC().Format(time.RFC3339)
		}
		if !definition.DisabledAt.IsZero() {
			updates["disabled-at"] = ""
		}
	case "disabled":
		if disabledAt, ok := lifecycleTransitionTime(definition.History, "disabled", true); ok {
			if definition.DisabledAt.IsZero() || !definition.DisabledAt.Equal(disabledAt) {
				updates["disabled-at"] = disabledAt.UTC().Format(time.RFC3339)
			}
		} else if definition.DisabledAt.IsZero() && latest.ToState == "disabled" {
			updates["disabled-at"] = latest.At.UTC().Format(time.RFC3339)
		}
		if !definition.ArchivedAt.IsZero() {
			updates["archived-at"] = ""
		}
	}
	return updates
}

func lifecycleTransitionTime(history []SkillHistoryEntry, toState string, preferLatest bool) (time.Time, bool) {
	var selected time.Time
	found := false
	for _, entry := range history {
		if entry.ToState != toState {
			continue
		}
		if !found {
			selected = entry.At.UTC()
			found = true
			continue
		}
		if preferLatest {
			if entry.At.After(selected) {
				selected = entry.At.UTC()
			}
			continue
		}
		if entry.At.Before(selected) {
			selected = entry.At.UTC()
		}
	}
	return selected, found
}

func isResolvedMissingState(state string) bool {
	return state == "missing"
}

func matchMissingCurrentEntry(entries []GovernanceLedgerEntry, target string) (GovernanceLedgerEntry, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return GovernanceLedgerEntry{}, errors.New("missing current target cannot be empty")
	}
	matches := make([]GovernanceLedgerEntry, 0, 1)
	for _, entry := range entries {
		if entry.SkillPath == trimmed || filepath.Base(entry.SkillPath) == trimmed || entry.SkillName == trimmed {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return GovernanceLedgerEntry{}, fmt.Errorf("missing current target %q not found", trimmed)
	}
	if len(matches) > 1 {
		return GovernanceLedgerEntry{}, fmt.Errorf("missing current target %q is ambiguous", trimmed)
	}
	return matches[0], nil
}

func governanceLedgerSkillKey(entry GovernanceLedgerEntry) string {
	return governanceRecordKey(entry.TemplateID, entry.SourceRunID, entry.SourceTaskID, entry.SkillName, entry.SkillPath)
}

func governanceDefinitionKey(definition Definition) string {
	return governanceRecordKey(definition.TemplateID, definition.SourceRunID, definition.SourceTaskID, definition.Name, definition.Path)
}

func governanceRecordKey(templateID string, sourceRunID string, sourceTaskID string, name string, path string) string {
	templateID = strings.TrimSpace(templateID)
	sourceRunID = strings.TrimSpace(sourceRunID)
	sourceTaskID = strings.TrimSpace(sourceTaskID)
	name = strings.TrimSpace(name)

	if sourceRunID != "" && sourceTaskID != "" {
		parts := []string{templateID, sourceRunID, sourceTaskID, name}
		return "source|" + strings.Join(parts, "|")
	}

	cleanedPath := strings.TrimSpace(path)
	if cleanedPath == "" {
		return "path|" + templateID + "||" + name
	}
	base := filepath.Base(filepath.Clean(cleanedPath))
	return "path|" + templateID + "|" + base + "|" + name
}

func resolveExternalSkillSourcePath(sourcePath string) (string, error) {
	trimmed := strings.TrimSpace(sourcePath)
	if trimmed == "" {
		return "", errors.New("restore source path cannot be empty")
	}
	cleaned := filepath.Clean(trimmed)
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve restore source path: %w", err)
	}
	if filepath.Ext(absPath) != ".md" {
		return "", errors.New("restore source path must point to a markdown skill file")
	}
	return absPath, nil
}

func (s *Store) resolveGovernedCurrentPath(skillPath string) (string, error) {
	if resolvedPath, err := s.resolveGeneratedPath(skillPath); err == nil {
		if _, statErr := os.Stat(resolvedPath); statErr == nil {
			return resolvedPath, nil
		}
	}
	if resolvedPath, err := s.resolveApprovedPath(skillPath); err == nil {
		if _, statErr := os.Stat(resolvedPath); statErr == nil {
			return resolvedPath, nil
		}
	}
	if resolvedPath, err := s.resolveArchivePath(skillPath); err == nil {
		if _, statErr := os.Stat(resolvedPath); statErr == nil {
			return resolvedPath, nil
		}
	}
	return "", errors.New("skill path must point to an existing markdown skill file inside skills/generated, skills/approved, or skills/archive")
}

func loadDefinitionFromContent(path string, content string) (Definition, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return Definition{}, fmt.Errorf("parse skill file %s: %w", path, err)
	}
	definition := Definition{Listing: Listing{Path: path}, Body: strings.TrimSpace(body)}
	for key, value := range frontmatter.scalars {
		switch key {
		case "name":
			definition.Name = value
		case "description":
			definition.Description = value
		case "when_to_use":
			definition.WhenToUse = value
		case "context":
			definition.Context = value
		case "version":
			definition.Version = value
		case "template-id":
			definition.TemplateID = value
		case "lifecycle-state":
			definition.LifecycleState = value
		case "source-run-id":
			definition.SourceRunID = value
		case "source-task-id":
			definition.SourceTaskID = value
		case "generated-at":
			definition.GeneratedAt, err = parseLifecycleTime(path, "generated-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "approved-at":
			definition.ApprovedAt, err = parseLifecycleTime(path, "approved-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "archived-at":
			definition.ArchivedAt, err = parseLifecycleTime(path, "archived-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "disabled-at":
			definition.DisabledAt, err = parseLifecycleTime(path, "disabled-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "user-invocable":
			definition.UserInvocable, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse user-invocable for %s: %w", path, err)
			}
		case "disable-model-invocation":
			definition.DisableModelInvocation, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse disable-model-invocation for %s: %w", path, err)
			}
		case "always-on":
			definition.AlwaysOn, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse always-on for %s: %w", path, err)
			}
		case "role":
			// S3.9: role was never parsed; always-on incorrectly overwrote Role.
			definition.Role = strings.TrimSpace(value)
		}
	}
	definition.AllowedTools = append(definition.AllowedTools, frontmatter.allowedTools...)
	if definition.Name == "" || definition.Description == "" || definition.WhenToUse == "" {
		return Definition{}, fmt.Errorf("skill file %s missing required listing fields", path)
	}
	if definition.LifecycleState == "" {
		definition.LifecycleState = inferredLifecycleState(path)
	}
	if definition.Role == "" && definition.TemplateID != "" {
		definition.Role = skillbuilder.ExtractRoleFromTemplateID(definition.TemplateID)
	}
	definition.Version = strings.TrimSuffix(definition.Version, "-candidate")
	definition.GovernanceAlerts = governanceAlertsForDefinition(definition)
	return definition, nil
}

func (s *Store) ensureLayout() error {
	for _, dir := range []string{s.templatesDir(), s.generatedDir(), s.approvedDir(), s.archiveDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create skills dir %s: %w", dir, err)
		}
	}
	return nil
}

func (s *Store) listGovernedDefinitions() ([]Definition, error) {
	loaders := []func() ([]Listing, error){s.ListGenerated, s.ListApproved, s.ListArchived, s.ListDisabled}
	definitions := make([]Definition, 0)
	for _, loader := range loaders {
		listings, err := loader()
		if err != nil {
			return nil, err
		}
		for _, listing := range listings {
			definition, err := s.readDefinition(listing.Path)
			if err != nil {
				return nil, err
			}
			definitions = append(definitions, definition)
		}
	}
	return definitions, nil
}

func (s *Store) templatesDir() string {
	return filepath.Join(s.root, "templates")
}

func (s *Store) generatedDir() string {
	return filepath.Join(s.root, "generated")
}

func (s *Store) approvedDir() string {
	return filepath.Join(s.root, "approved")
}

func (s *Store) archiveDir() string {
	return filepath.Join(s.root, "archive")
}

func (s *Store) resolveGeneratedPath(candidatePath string) (string, error) {
	trimmed := strings.TrimSpace(candidatePath)
	if trimmed == "" {
		return "", errors.New("generated skill path cannot be empty")
	}

	path := filepath.Clean(trimmed)
	if !filepath.IsAbs(path) && filepath.Dir(path) == "." {
		path = filepath.Join(s.generatedDir(), path)
	}

	absCandidate, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve generated skill path: %w", err)
	}
	absGeneratedDir, err := filepath.Abs(s.generatedDir())
	if err != nil {
		return "", fmt.Errorf("resolve generated dir: %w", err)
	}
	rel, err := filepath.Rel(absGeneratedDir, absCandidate)
	if err != nil {
		return "", fmt.Errorf("rel generated skill path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", errors.New("generated skill path must be inside skills/generated")
	}
	if filepath.Ext(absCandidate) != ".md" {
		return "", errors.New("generated skill path must point to a markdown skill file")
	}
	return absCandidate, nil
}

func (s *Store) resolveApprovedPath(approvedPath string) (string, error) {
	trimmed := strings.TrimSpace(approvedPath)
	if trimmed == "" {
		return "", errors.New("approved skill path cannot be empty")
	}

	path := filepath.Clean(trimmed)
	if !filepath.IsAbs(path) && filepath.Dir(path) == "." {
		path = filepath.Join(s.approvedDir(), path)
	}

	absApprovedPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve approved skill path: %w", err)
	}
	absApprovedDir, err := filepath.Abs(s.approvedDir())
	if err != nil {
		return "", fmt.Errorf("resolve approved dir: %w", err)
	}
	rel, err := filepath.Rel(absApprovedDir, absApprovedPath)
	if err != nil {
		return "", fmt.Errorf("rel approved skill path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", errors.New("approved skill path must be inside skills/approved")
	}
	if filepath.Ext(absApprovedPath) != ".md" {
		return "", errors.New("approved skill path must point to a markdown skill file")
	}
	return absApprovedPath, nil
}

func (s *Store) resolveArchivePath(archivedPath string) (string, error) {
	trimmed := strings.TrimSpace(archivedPath)
	if trimmed == "" {
		return "", errors.New("archived skill path cannot be empty")
	}

	path := filepath.Clean(trimmed)
	if !filepath.IsAbs(path) && filepath.Dir(path) == "." {
		path = filepath.Join(s.archiveDir(), path)
	}

	absArchivedPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve archived skill path: %w", err)
	}
	absArchiveDir, err := filepath.Abs(s.archiveDir())
	if err != nil {
		return "", fmt.Errorf("resolve archive dir: %w", err)
	}
	rel, err := filepath.Rel(absArchiveDir, absArchivedPath)
	if err != nil {
		return "", fmt.Errorf("rel archived skill path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", errors.New("archived skill path must be inside skills/archive")
	}
	if filepath.Ext(absArchivedPath) != ".md" {
		return "", errors.New("archived skill path must point to a markdown skill file")
	}
	return absArchivedPath, nil
}

func (s *Store) resolveApprovedOrArchivePath(skillPath string) (string, error) {
	approvedPath, approvedErr := s.resolveApprovedPath(skillPath)
	if approvedErr == nil {
		if _, err := os.Stat(approvedPath); err == nil {
			return approvedPath, nil
		}
	}
	archivedPath, archivedErr := s.resolveArchivePath(skillPath)
	if archivedErr == nil {
		if _, err := os.Stat(archivedPath); err == nil {
			return archivedPath, nil
		}
	}
	if approvedErr == nil {
		return "", fmt.Errorf("skill file %s not found in skills/approved or skills/archive", skillPath)
	}
	if archivedErr == nil {
		return "", fmt.Errorf("skill file %s not found in skills/approved or skills/archive", skillPath)
	}
	return "", fmt.Errorf("resolve skill inspection path: %w", approvedErr)
}

func sanitizeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "skill"
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", ".", "-")
	return replacer.Replace(value)
}

func listMarkdownFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	return files, nil
}

func (s *Store) listByDir(dir string, defaultLifecycleState string, filterLifecycleState string) ([]Listing, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	listings := make([]Listing, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		listing, err := s.readListing(path)
		if err != nil {
			return nil, err
		}
		if listing.LifecycleState == "" {
			listing.LifecycleState = defaultLifecycleState
		}
		if filterLifecycleState != "" && listing.LifecycleState != filterLifecycleState {
			continue
		}
		listings = append(listings, listing)
	}

	sort.Slice(listings, func(i int, j int) bool {
		return listings[i].Path < listings[j].Path
	})
	return listings, nil
}

func (s *Store) listByDirTolerant(dir string, defaultLifecycleState string, filterLifecycleState string) (ListingScan, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ListingScan{}, fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	scan := ListingScan{Listings: make([]Listing, 0, len(entries))}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		listing, err := s.readListing(path)
		if err != nil {
			scan.Warnings = append(scan.Warnings, ListingWarning{Path: path, Error: err.Error()})
			continue
		}
		if listing.LifecycleState == "" {
			listing.LifecycleState = defaultLifecycleState
		}
		if filterLifecycleState != "" && listing.LifecycleState != filterLifecycleState {
			continue
		}
		scan.Listings = append(scan.Listings, listing)
	}

	sort.Slice(scan.Listings, func(i int, j int) bool {
		return scan.Listings[i].Path < scan.Listings[j].Path
	})
	sort.Slice(scan.Warnings, func(i int, j int) bool {
		return scan.Warnings[i].Path < scan.Warnings[j].Path
	})
	return scan, nil
}

func (s *Store) readListing(path string) (Listing, error) {
	definition, err := s.readDefinition(path)
	if err != nil {
		return Listing{}, err
	}
	return definition.Listing, nil
}

func (s *Store) readDefinition(path string) (Definition, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("read skill file %s: %w", path, err)
	}

	frontmatter, body, err := splitFrontmatter(string(content))
	if err != nil {
		return Definition{}, fmt.Errorf("parse skill file %s: %w", path, err)
	}

	definition := Definition{
		Listing: Listing{Path: path},
		Body:    strings.TrimSpace(body),
	}
	history, err := readSkillHistory(path)
	if err != nil {
		definition.HistoryError = err.Error()
	} else {
		definition.History = history
	}
	for key, value := range frontmatter.scalars {
		switch key {
		case "name":
			definition.Name = value
		case "description":
			definition.Description = value
		case "when_to_use":
			definition.WhenToUse = value
		case "context":
			definition.Context = value
		case "version":
			definition.Version = value
		case "template-id":
			definition.TemplateID = value
		case "lifecycle-state":
			definition.LifecycleState = value
		case "source-run-id":
			definition.SourceRunID = value
		case "source-task-id":
			definition.SourceTaskID = value
		case "generated-at":
			definition.GeneratedAt, err = parseLifecycleTime(path, "generated-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "approved-at":
			definition.ApprovedAt, err = parseLifecycleTime(path, "approved-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "archived-at":
			definition.ArchivedAt, err = parseLifecycleTime(path, "archived-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "disabled-at":
			definition.DisabledAt, err = parseLifecycleTime(path, "disabled-at", value)
			if err != nil {
				return Definition{}, err
			}
		case "user-invocable":
			definition.UserInvocable, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse user-invocable for %s: %w", path, err)
			}
		case "disable-model-invocation":
			definition.DisableModelInvocation, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse disable-model-invocation for %s: %w", path, err)
			}
		case "always-on":
			definition.AlwaysOn, err = strconv.ParseBool(value)
			if err != nil {
				return Definition{}, fmt.Errorf("parse always-on for %s: %w", path, err)
			}
		case "role":
			definition.Role = strings.TrimSpace(value)
		}
	}
	definition.AllowedTools = append(definition.AllowedTools, frontmatter.allowedTools...)

	if definition.Name == "" || definition.Description == "" || definition.WhenToUse == "" {
		return Definition{}, fmt.Errorf("skill file %s missing required listing fields", path)
	}
	if definition.LifecycleState == "" {
		definition.LifecycleState = inferredLifecycleState(path)
	}
	if definition.Role == "" && definition.TemplateID != "" {
		definition.Role = skillbuilder.ExtractRoleFromTemplateID(definition.TemplateID)
	}
	definition.Version = strings.TrimSuffix(definition.Version, "-candidate")
	definition.GovernanceAlerts = governanceAlertsForDefinition(definition)
	return definition, nil
}

func skillHistoryPath(skillPath string) string {
	ext := filepath.Ext(skillPath)
	return strings.TrimSuffix(skillPath, ext) + ".history.json"
}

func readSkillHistory(skillPath string) ([]SkillHistoryEntry, error) {
	historyPath := skillHistoryPath(skillPath)
	content, err := os.ReadFile(historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill history %s: %w", historyPath, err)
	}
	var history []SkillHistoryEntry
	if err := json.Unmarshal(content, &history); err != nil {
		return nil, fmt.Errorf("decode skill history %s: %w", historyPath, err)
	}
	return history, nil
}

func writeSkillHistory(skillPath string, history []SkillHistoryEntry) error {
	historyPath := skillHistoryPath(skillPath)
	content, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("encode skill history %s: %w", historyPath, err)
	}
	if err := os.WriteFile(historyPath, append(content, '\n'), 0o644); err != nil {
		return fmt.Errorf("write skill history %s: %w", historyPath, err)
	}
	return nil
}

func appendSkillHistory(currentSkillPath string, nextSkillPath string, entry SkillHistoryEntry) error {
	history, err := readSkillHistory(currentSkillPath)
	if err != nil {
		return err
	}
	history = append(history, entry)
	if currentSkillPath != nextSkillPath {
		currentHistoryPath := skillHistoryPath(currentSkillPath)
		nextHistoryPath := skillHistoryPath(nextSkillPath)
		if _, statErr := os.Stat(currentHistoryPath); statErr == nil {
			if err := os.Rename(currentHistoryPath, nextHistoryPath); err != nil {
				return fmt.Errorf("move skill history %s -> %s: %w", currentHistoryPath, nextHistoryPath, err)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat skill history %s: %w", currentHistoryPath, statErr)
		}
	}
	return writeSkillHistory(nextSkillPath, history)
}

// RecordSkillUse appends a task-completed entry to the governance ledger
// with success/failure tracking. P1-4b: Skill performance feedback.
func (s *Store) RecordSkillUse(skillName string, skillPath string, taskID string, success bool) error {
	verdict := "success"
	if !success {
		verdict = "failure"
	}
	return s.appendGovernanceLedgerEntry(GovernanceLedgerEntry{
		At:           time.Now().UTC(),
		Action:       "task_completed",
		ToState:      verdict,
		SkillName:    skillName,
		SkillPath:    skillPath,
		SourceTaskID: taskID,
	})
}

func (s *Store) governanceLedgerPath() string {
	return filepath.Join(s.root, "governance-ledger.jsonl")
}

func (s *Store) readGovernanceLedgerEntries() ([]GovernanceLedgerEntry, error) {
	content, err := os.ReadFile(s.governanceLedgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read governance ledger %s: %w", s.governanceLedgerPath(), err)
	}
	lines := strings.Split(string(content), "\n")
	entries := make([]GovernanceLedgerEntry, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var entry GovernanceLedgerEntry
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			return nil, fmt.Errorf("decode governance ledger entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *Store) appendGovernanceLedgerEntry(entry GovernanceLedgerEntry) error {
	content, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode governance ledger entry: %w", err)
	}
	file, err := os.OpenFile(s.governanceLedgerPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open governance ledger %s: %w", s.governanceLedgerPath(), err)
	}
	defer func() {
		_ = file.Close()
	}()
	if _, err := file.Write(append(content, '\n')); err != nil {
		return fmt.Errorf("append governance ledger %s: %w", s.governanceLedgerPath(), err)
	}
	return nil
}

func parseLifecycleTime(path string, field string, value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s for %s: %w", field, path, err)
	}
	return parsed.UTC(), nil
}

func inferredLifecycleState(path string) string {
	switch filepath.Base(filepath.Dir(path)) {
	case "approved":
		return "approved"
	case "generated":
		return "generated"
	case "archive":
		return "archived"
	case "templates":
		return "template"
	default:
		return ""
	}
}

func (s *Store) findReviewReference(candidate Definition) (Definition, string, bool, error) {
	type reviewedReference struct {
		definition Definition
		reason     string
		priority   int
	}
	var best reviewedReference
	bestFound := false
	for _, loader := range []func() ([]Listing, error){s.ListApproved, s.ListDisabled, s.ListArchived} {
		listings, err := loader()
		if err != nil {
			return Definition{}, "", false, err
		}
		for _, listing := range listings {
			definition, err := s.LoadInspect(listing.Path)
			if err != nil {
				return Definition{}, "", false, err
			}
			priority, reason := reviewReferencePriority(candidate, definition)
			if priority == 0 {
				continue
			}
			if !bestFound || priority > best.priority {
				best = reviewedReference{definition: definition, reason: reason, priority: priority}
				bestFound = true
			}
		}
	}
	if !bestFound {
		return Definition{}, "", false, nil
	}
	return best.definition, best.reason, true, nil
}

func reviewReferencePriority(candidate Definition, reference Definition) (int, string) {
	if candidate.TemplateID != "" && candidate.TemplateID == reference.TemplateID {
		return 200, fmt.Sprintf("template-id: %s", candidate.TemplateID)
	}
	if candidate.Name != "" && candidate.Name == reference.Name {
		return 100, fmt.Sprintf("name: %s", candidate.Name)
	}
	return 0, ""
}

func buildReviewFieldDiffs(candidate Definition, reference Definition) []ReviewFieldDiff {
	comparisons := []ReviewFieldDiff{
		{Field: "Name", CandidateValue: candidate.Name, ReferenceValue: reference.Name},
		{Field: "Description", CandidateValue: candidate.Description, ReferenceValue: reference.Description},
		{Field: "WhenToUse", CandidateValue: candidate.WhenToUse, ReferenceValue: reference.WhenToUse},
		{Field: "Context", CandidateValue: candidate.Context, ReferenceValue: reference.Context},
		{Field: "Version", CandidateValue: candidate.Version, ReferenceValue: reference.Version},
		{Field: "TemplateID", CandidateValue: candidate.TemplateID, ReferenceValue: reference.TemplateID},
		{Field: "AllowedTools", CandidateValue: strings.Join(candidate.AllowedTools, ", "), ReferenceValue: strings.Join(reference.AllowedTools, ", ")},
		{Field: "UserInvocable", CandidateValue: strconv.FormatBool(candidate.UserInvocable), ReferenceValue: strconv.FormatBool(reference.UserInvocable)},
		{Field: "DisableModelInvocation", CandidateValue: strconv.FormatBool(candidate.DisableModelInvocation), ReferenceValue: strconv.FormatBool(reference.DisableModelInvocation)},
	}
	diffs := make([]ReviewFieldDiff, 0, len(comparisons))
	for _, comparison := range comparisons {
		if comparison.CandidateValue != comparison.ReferenceValue {
			diffs = append(diffs, comparison)
		}
	}
	return diffs
}

func validateGeneratedCandidate(candidate Definition) []string {
	findings := []string{}
	if strings.TrimSpace(candidate.Name) == "" {
		findings = append(findings, "missing name")
	}
	if len(strings.TrimSpace(candidate.Description)) < 24 {
		findings = append(findings, "description is too short")
	}
	if len(strings.TrimSpace(candidate.WhenToUse)) < 24 {
		findings = append(findings, "when_to_use is too short")
	}
	if strings.TrimSpace(candidate.Context) == "" {
		findings = append(findings, "missing context")
	}
	if strings.TrimSpace(candidate.TemplateID) == "" {
		findings = append(findings, "missing template-id")
	}
	if len(candidate.AllowedTools) == 0 {
		findings = append(findings, "missing allowed-tools")
	}
	schema := validationSchemaForTemplate(candidate.TemplateID)
	for _, tool := range candidate.AllowedTools {
		trimmed := strings.TrimSpace(tool)
		if !schema.allowedToolOK(trimmed) {
			findings = append(findings, "allowed-tools contains disallowed tool: "+tool)
		}
	}
	if candidate.UserInvocable {
		findings = append(findings, "generated candidates must not be user-invocable before approval")
	}
	if !candidate.DisableModelInvocation {
		findings = append(findings, "generated candidates must disable model invocation before approval")
	}
	body := strings.TrimSpace(candidate.Body)
	if countTextLines(body) < schema.minBodyLines {
		findings = append(findings, "body is too short")
	}
	for _, heading := range schema.requiredHeadings {
		if !strings.Contains(body, heading) {
			findings = append(findings, "body missing section: "+heading)
		}
	}
	return dedupeStrings(findings)
}

// candidateValidationSchema selects required body sections / tool policy by
// template-id. S3.7: role skills were killed by Task Survey's five headings.
type candidateValidationSchema struct {
	requiredHeadings []string
	minBodyLines     int
	readonlyOnly     bool
	extraTools       map[string]bool
}

func validationSchemaForTemplate(templateID string) candidateValidationSchema {
	id := strings.ToLower(strings.TrimSpace(templateID))
	role := skillbuilder.ExtractRoleFromTemplateID(id)
	switch {
	case strings.Contains(id, "task-survey"):
		return candidateValidationSchema{
			requiredHeadings: []string{
				"## Purpose",
				"## Task Focus",
				"## Repo Survey Summary",
				"## Suggested Workflow",
				"## Constraints",
			},
			minBodyLines: 12,
			readonlyOnly: true,
		}
	case strings.Contains(id, "attached-file"):
		return candidateValidationSchema{
			requiredHeadings: []string{"## Purpose", "## Constraints"},
			minBodyLines:     8,
			readonlyOnly:     true,
		}
	case role != "":
		// Role skills share Purpose + Constraints; Builder may use write tools.
		extra := map[string]bool{}
		readonly := true
		switch role {
		case "builder":
			readonly = false
			extra["write"] = true
			extra["precise_edit"] = true
			extra["patch"] = true
		case "critic":
			readonly = false
			extra["precise_edit"] = true
		case "synthesizer":
			readonly = false
			extra["precise_edit"] = true
		}
		return candidateValidationSchema{
			requiredHeadings: []string{"## Purpose", "## Constraints"},
			minBodyLines:     8,
			readonlyOnly:     readonly,
			extraTools:       extra,
		}
	default:
		return candidateValidationSchema{
			requiredHeadings: []string{"## Purpose", "## Constraints"},
			minBodyLines:     8,
			readonlyOnly:     true,
		}
	}
}

func (s candidateValidationSchema) allowedToolOK(tool string) bool {
	switch tool {
	case "read", "memory", "search":
		return true
	}
	if s.extraTools != nil && s.extraTools[tool] {
		return true
	}
	if s.readonlyOnly {
		return false
	}
	// Non-readonly schemas still reject shell/git by default.
	switch tool {
	case "write", "precise_edit", "patch":
		return true
	default:
		return false
	}
}

func countTextLines(value string) int {
	trimmed := normalizeSkillBody(value)
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func normalizeSkillBody(value string) string {
	return strings.TrimSpace(value)
}

func buildBodyDiffLines(candidateBody string, referenceBody string) []string {
	candidateLines := splitNormalizedLines(candidateBody)
	referenceLines := splitNormalizedLines(referenceBody)
	if len(candidateLines) == 0 && len(referenceLines) == 0 {
		return nil
	}
	return formatUnifiedBodyDiff(buildBodyDiffOps(referenceLines, candidateLines), 3)
}

func buildBodyDiffOps(referenceLines []string, candidateLines []string) []bodyDiffOp {
	lcs := make([][]int, len(referenceLines)+1)
	for index := range lcs {
		lcs[index] = make([]int, len(candidateLines)+1)
	}
	for refIndex := 1; refIndex <= len(referenceLines); refIndex++ {
		for candidateIndex := 1; candidateIndex <= len(candidateLines); candidateIndex++ {
			if referenceLines[refIndex-1] == candidateLines[candidateIndex-1] {
				lcs[refIndex][candidateIndex] = lcs[refIndex-1][candidateIndex-1] + 1
				continue
			}
			if lcs[refIndex-1][candidateIndex] >= lcs[refIndex][candidateIndex-1] {
				lcs[refIndex][candidateIndex] = lcs[refIndex-1][candidateIndex]
				continue
			}
			lcs[refIndex][candidateIndex] = lcs[refIndex][candidateIndex-1]
		}
	}
	ops := make([]bodyDiffOp, 0, len(referenceLines)+len(candidateLines))
	refIndex := len(referenceLines)
	candidateIndex := len(candidateLines)
	for refIndex > 0 || candidateIndex > 0 {
		switch {
		case refIndex > 0 && candidateIndex > 0 && referenceLines[refIndex-1] == candidateLines[candidateIndex-1]:
			ops = append(ops, bodyDiffOp{kind: ' ', line: referenceLines[refIndex-1], referenceLine: refIndex, candidateLine: candidateIndex})
			refIndex--
			candidateIndex--
		case candidateIndex > 0 && (refIndex == 0 || lcs[refIndex][candidateIndex-1] >= lcs[refIndex-1][candidateIndex]):
			ops = append(ops, bodyDiffOp{kind: '+', line: candidateLines[candidateIndex-1], candidateLine: candidateIndex})
			candidateIndex--
		case refIndex > 0:
			ops = append(ops, bodyDiffOp{kind: '-', line: referenceLines[refIndex-1], referenceLine: refIndex})
			refIndex--
		}
	}
	for left, right := 0, len(ops)-1; left < right; left, right = left+1, right-1 {
		ops[left], ops[right] = ops[right], ops[left]
	}
	return ops
}

func formatUnifiedBodyDiff(ops []bodyDiffOp, contextLines int) []string {
	changeIndexes := make([]int, 0)
	for index, op := range ops {
		if op.kind != ' ' {
			changeIndexes = append(changeIndexes, index)
		}
	}
	if len(changeIndexes) == 0 {
		return nil
	}

	ranges := make([][2]int, 0, len(changeIndexes))
	start := maxInt(0, changeIndexes[0]-contextLines)
	end := minInt(len(ops)-1, changeIndexes[0]+contextLines)
	for _, index := range changeIndexes[1:] {
		rangeStart := maxInt(0, index-contextLines)
		rangeEnd := minInt(len(ops)-1, index+contextLines)
		if rangeStart <= end+1 {
			if rangeEnd > end {
				end = rangeEnd
			}
			continue
		}
		ranges = append(ranges, [2]int{start, end})
		start = rangeStart
		end = rangeEnd
	}
	ranges = append(ranges, [2]int{start, end})

	lines := make([]string, 0, len(ops)+len(ranges))
	for _, hunkRange := range ranges {
		hunk := ops[hunkRange[0] : hunkRange[1]+1]
		lines = append(lines, formatUnifiedBodyDiffHeader(hunk))
		for _, op := range hunk {
			lines = append(lines, string(op.kind)+" "+op.line)
		}
	}
	return lines
}

func formatUnifiedBodyDiffHeader(hunk []bodyDiffOp) string {
	referenceStart := 0
	candidateStart := 0
	referenceCount := 0
	candidateCount := 0
	for _, op := range hunk {
		if referenceStart == 0 && op.referenceLine > 0 {
			referenceStart = op.referenceLine
		}
		if candidateStart == 0 && op.candidateLine > 0 {
			candidateStart = op.candidateLine
		}
		if op.kind != '+' {
			referenceCount++
		}
		if op.kind != '-' {
			candidateCount++
		}
	}
	if referenceStart == 0 {
		referenceStart = candidateStart
		if referenceStart == 0 {
			referenceStart = 1
		}
	}
	if candidateStart == 0 {
		candidateStart = referenceStart
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", referenceStart, referenceCount, candidateStart, candidateCount)
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func splitNormalizedLines(value string) []string {
	trimmed := normalizeSkillBody(value)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// frontmatterHasKey checks whether a YAML frontmatter block contains a given key.
func frontmatterHasKey(content string, key string) bool {
	parsed, _, err := splitFrontmatter(content)
	if err != nil {
		return false
	}
	_, ok := parsed.scalars[key]
	return ok
}

func rewriteFrontmatterScalars(content string, updates map[string]string) (string, error) {
	parsed, body, err := splitFrontmatter(content)
	if err != nil {
		return "", err
	}
	for key, value := range updates {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			delete(parsed.scalars, key)
			continue
		}
		parsed.scalars[key] = trimmed
	}
	return renderSkillFile(parsed, body), nil
}

func renderSkillFile(frontmatter parsedFrontmatter, body string) string {
	orderedKeys := []string{
		"name",
		"description",
		"when_to_use",
		"context",
		"user-invocable",
		"disable-model-invocation",
		"always-on",
		"version",
		"template-id",
		"lifecycle-state",
		"source-run-id",
		"source-task-id",
		"generated-at",
		"approved-at",
		"archived-at",
		"disabled-at",
	}
	var builder strings.Builder
	builder.WriteString("---\n")
	for _, key := range orderedKeys {
		if value, ok := frontmatter.scalars[key]; ok {
			builder.WriteString(key)
			builder.WriteString(": ")
			builder.WriteString(value)
			builder.WriteString("\n")
		}
	}
	if len(frontmatter.allowedTools) > 0 {
		builder.WriteString("allowed-tools:\n")
		for _, tool := range frontmatter.allowedTools {
			builder.WriteString("  - ")
			builder.WriteString(tool)
			builder.WriteString("\n")
		}
	}
	extras := make([]string, 0, len(frontmatter.scalars))
	for key := range frontmatter.scalars {
		if containsFrontmatterKey(orderedKeys, key) {
			continue
		}
		extras = append(extras, key)
	}
	sort.Strings(extras)
	for _, key := range extras {
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(frontmatter.scalars[key])
		builder.WriteString("\n")
	}
	builder.WriteString("---\n\n")
	builder.WriteString(strings.TrimSpace(body))
	builder.WriteString("\n")
	return builder.String()
}

func containsFrontmatterKey(keys []string, target string) bool {
	for _, key := range keys {
		if key == target {
			return true
		}
	}
	return false
}

type parsedFrontmatter struct {
	scalars      map[string]string
	allowedTools []string
}

func splitFrontmatter(content string) (parsedFrontmatter, string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return parsedFrontmatter{}, "", errors.New("missing frontmatter")
	}

	parsed := parsedFrontmatter{scalars: make(map[string]string)}
	inAllowedTools := false
	bodyStart := -1
	for index, rawLine := range lines[1:] {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			bodyStart = index + 2
			break
		}
		if trimmed == "" {
			continue
		}

		if inAllowedTools {
			if strings.HasPrefix(trimmed, "-") {
				tool := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
				if tool != "" {
					parsed.allowedTools = append(parsed.allowedTools, tool)
				}
				continue
			}
			inAllowedTools = false
		}

		if trimmed == "allowed-tools:" {
			inAllowedTools = true
			continue
		}

		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		parsed.scalars[key] = strings.TrimSpace(value)
	}

	if bodyStart == -1 {
		return parsedFrontmatter{}, "", errors.New("unterminated frontmatter")
	}
	return parsed, strings.Join(lines[bodyStart:], "\n"), nil
}
