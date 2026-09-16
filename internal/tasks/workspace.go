package tasks

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const manifestFileName = "task.json"

type Workspace struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Status           string    `json:"status"`
	LatestTranscript string    `json:"latest_transcript"`
	LatestSummary    string    `json:"latest_summary"`
	RunCount         int       `json:"run_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	RootDir      string `json:"-"`
	SessionsDir  string `json:"-"`
	MemoryDir    string `json:"-"`
	ManifestPath string `json:"-"`
}

type ResolveOptions struct {
	ID       string
	Title    string
	ForceNew bool
}

type Manager struct {
	BaseDir string
}

func NewManager(baseDir string) *Manager {
	return &Manager{BaseDir: baseDir}
}

func (m *Manager) Resolve(options ResolveOptions) (Workspace, bool, error) {
	if m == nil {
		return Workspace{}, false, fmt.Errorf("task manager is required")
	}
	baseID := sanitizeTaskID(options.ID)
	if baseID == "" {
		baseID = deriveTaskID(options.Title)
	}

	if options.ForceNew {
		if sanitizeTaskID(options.ID) != "" {
			if _, err := m.Load(baseID); err == nil {
				return Workspace{}, false, fmt.Errorf("task %q already exists", baseID)
			} else if !os.IsNotExist(err) {
				return Workspace{}, false, err
			}
			return m.create(baseID, options.Title)
		}

		candidate := baseID
		for attempt := 0; ; attempt++ {
			if attempt > 0 {
				candidate = fmt.Sprintf("%s-%s", baseID, time.Now().UTC().Format("20060102-150405"))
			}
			if _, err := m.Load(candidate); os.IsNotExist(err) {
				return m.create(candidate, options.Title)
			} else if err != nil {
				return Workspace{}, false, err
			}
		}
	}

	workspace, err := m.Load(baseID)
	if err == nil {
		return workspace, false, nil
	}
	if !os.IsNotExist(err) {
		return Workspace{}, false, err
	}
	return m.create(baseID, options.Title)
}

func (m *Manager) Load(taskID string) (Workspace, error) {
	if m == nil {
		return Workspace{}, fmt.Errorf("task manager is required")
	}
	// Batch5/O: load by literal directory name first. sanitizeTaskID truncates
	// to 48 chars — that broke List/Load/resume for long existing task IDs
	// (IsNotExist → silent skip). Only fall back to sanitized for legacy shorts.
	id := strings.TrimSpace(taskID)
	if id == "" {
		return Workspace{}, fmt.Errorf("task id is required")
	}
	workspace := m.workspaceShell(id)
	content, err := os.ReadFile(workspace.ManifestPath)
	if err != nil {
		if san := sanitizeTaskID(id); san != "" && san != id {
			workspace = m.workspaceShell(san)
			content, err = os.ReadFile(workspace.ManifestPath)
		}
		if err != nil {
			return Workspace{}, err
		}
	}
	if err := json.Unmarshal(content, &workspace); err != nil {
		return Workspace{}, fmt.Errorf("decode task manifest: %w", err)
	}
	// Preserve on-disk directory id (may differ from json "id" after truncate bugs).
	if workspace.ID == "" {
		workspace.ID = id
	}
	return workspace, nil
}

func (m *Manager) List() ([]Workspace, error) {
	if m == nil {
		return nil, fmt.Errorf("task manager is required")
	}
	entries, err := os.ReadDir(m.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Workspace{}, nil
		}
		return nil, fmt.Errorf("read task workspace directory: %w", err)
	}

	workspaces := make([]Workspace, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workspace, err := m.Load(entry.Name())
		if err != nil {
			// Batch4/L: surface corrupt manifests instead of silently dropping.
			if !os.IsNotExist(err) {
				stub := m.workspaceShell(sanitizeTaskID(entry.Name()))
				stub.ID = entry.Name()
				stub.Title = entry.Name()
				stub.Status = "corrupt"
				stub.LatestSummary = "task.json unreadable: " + err.Error()
				if info, statErr := entry.Info(); statErr == nil {
					stub.UpdatedAt = info.ModTime().UTC()
				}
				stub = m.reconcileFromSessions(stub)
				workspaces = append(workspaces, stub)
			}
			continue
		}
		workspace = m.reconcileFromSessions(workspace)
		workspaces = append(workspaces, workspace)
	}

	sort.Slice(workspaces, func(i int, j int) bool {
		if workspaces[i].UpdatedAt.Equal(workspaces[j].UpdatedAt) {
			return workspaces[i].ID < workspaces[j].ID
		}
		return workspaces[i].UpdatedAt.After(workspaces[j].UpdatedAt)
	})
	return workspaces, nil
}

// reconcileFromSessions heals task.json when sessions exist but RunCount/Status
// were never updated (killed runs, crashed MarkRun). Persists when healed.
func (m *Manager) reconcileFromSessions(workspace Workspace) Workspace {
	sessions, err := os.ReadDir(workspace.SessionsDir)
	if err != nil {
		return workspace
	}
	var newestName string
	var newestMod time.Time
	count := 0
	for _, s := range sessions {
		if s.IsDir() || !strings.HasSuffix(s.Name(), ".jsonl") {
			continue
		}
		count++
		info, err := s.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newestName = s.Name()
		}
	}
	if count == 0 {
		return workspace
	}
	dirty := false
	if workspace.RunCount < count {
		workspace.RunCount = count
		dirty = true
	}
	if strings.TrimSpace(workspace.LatestTranscript) == "" && newestName != "" {
		workspace.LatestTranscript = filepath.ToSlash(filepath.Join("sessions", newestName))
		dirty = true
	}
	if workspace.Status == "new" || workspace.Status == "" {
		workspace.Status = "interrupted"
		dirty = true
	}
	if !newestMod.IsZero() && (workspace.UpdatedAt.IsZero() || newestMod.After(workspace.UpdatedAt)) {
		workspace.UpdatedAt = newestMod.UTC()
		dirty = true
	}
	if dirty {
		_ = m.save(workspace)
	}
	return workspace
}

func (m *Manager) Delete(taskID string) error {
	if m == nil {
		return fmt.Errorf("task manager is required")
	}
	workspaceID := strings.TrimSpace(taskID)
	if workspaceID == "" {
		return fmt.Errorf("task id is required")
	}
	workspace := m.workspaceShell(workspaceID)
	if _, err := os.Stat(workspace.ManifestPath); err != nil {
		if os.IsNotExist(err) {
			if san := sanitizeTaskID(workspaceID); san != "" && san != workspaceID {
				workspace = m.workspaceShell(san)
				if _, err2 := os.Stat(workspace.ManifestPath); err2 != nil {
					return err
				}
			} else {
				return err
			}
		} else {
			return fmt.Errorf("stat task manifest: %w", err)
		}
	}
	if err := os.RemoveAll(workspace.RootDir); err != nil {
		return fmt.Errorf("delete task workspace: %w", err)
	}
	return nil
}

func (m *Manager) LoadByTranscript(transcriptPath string) (Workspace, bool, error) {
	if m == nil {
		return Workspace{}, false, fmt.Errorf("task manager is required")
	}
	absBase, err := filepath.Abs(m.BaseDir)
	if err != nil {
		return Workspace{}, false, err
	}
	absTranscript, err := filepath.Abs(filepath.Clean(transcriptPath))
	if err != nil {
		return Workspace{}, false, err
	}
	relative, err := filepath.Rel(absBase, absTranscript)
	if err != nil {
		return Workspace{}, false, nil
	}
	relative = filepath.ToSlash(relative)
	if strings.HasPrefix(relative, "../") || relative == ".." {
		return Workspace{}, false, nil
	}
	parts := strings.Split(relative, "/")
	if len(parts) < 3 || parts[1] != "sessions" {
		return Workspace{}, false, nil
	}
	workspace, err := m.Load(parts[0])
	if err != nil {
		if os.IsNotExist(err) {
			return Workspace{}, false, nil
		}
		return Workspace{}, false, err
	}
	return workspace, true, nil
}

func (m *Manager) MarkRun(workspace Workspace, transcriptPath string, summary string) (Workspace, error) {
	return m.MarkRunOutcome(workspace, transcriptPath, summary, "")
}

// MarkRunOutcome records a completed run with an explicit status. Empty status
// is inferred from the summary (Batch3/P11).
func (m *Manager) MarkRunOutcome(workspace Workspace, transcriptPath string, summary string, status string) (Workspace, error) {
	if workspace.ID == "" {
		return Workspace{}, fmt.Errorf("task workspace id is required")
	}
	relativeTranscript := transcriptPath
	if rel, err := filepath.Rel(workspace.RootDir, transcriptPath); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		relativeTranscript = filepath.ToSlash(rel)
	}
	workspace.LatestTranscript = relativeTranscript
	workspace.LatestSummary = sanitizeTaskSummary(summary)
	if strings.TrimSpace(status) == "" {
		status = inferTaskRunStatus(summary)
	}
	workspace.Status = status
	workspace.RunCount++
	workspace.UpdatedAt = time.Now().UTC()
	if workspace.CreatedAt.IsZero() {
		workspace.CreatedAt = workspace.UpdatedAt
	}
	return workspace, m.save(workspace)
}

// sanitizeTaskSummary caps length and strips control chars that historically
// corrupted hand-edited or partially-written task.json manifests (Batch4/L).
func sanitizeTaskSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return summary
	}
	var b strings.Builder
	b.Grow(len(summary))
	for _, r := range summary {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	const maxSummaryRunes = 2000
	runes := []rune(out)
	if len(runes) > maxSummaryRunes {
		out = string(runes[:maxSummaryRunes-3]) + "..."
	}
	return out
}

// inferTaskRunStatus maps a run summary onto a durable workspace status.
func inferTaskRunStatus(summary string) string {
	lower := strings.ToLower(summary)
	switch {
	case strings.Contains(lower, "build_ok=false"),
		strings.Contains(lower, "verification failed"),
		strings.Contains(lower, "verifier: fail"),
		strings.Contains(lower, "run status: failed"),
		strings.Contains(lower, "avatar id is required"):
		return "failed"
	case strings.Contains(lower, "completed_unverified"),
		strings.Contains(lower, "needs_remediation"),
		strings.Contains(lower, "post_build_failed"):
		return "completed_unverified"
	case strings.Contains(lower, "interrupted"),
		strings.Contains(lower, "no resumable boundary"):
		return "interrupted"
	default:
		return "stable"
	}
}

func (m *Manager) create(taskID string, title string) (Workspace, bool, error) {
	workspace := m.workspaceShell(taskID)
	now := time.Now().UTC()
	workspace.Title = strings.TrimSpace(title)
	workspace.Status = "new"
	workspace.CreatedAt = now
	workspace.UpdatedAt = now
	if err := os.MkdirAll(workspace.SessionsDir, 0o755); err != nil {
		return Workspace{}, false, fmt.Errorf("create task sessions directory: %w", err)
	}
	if err := os.MkdirAll(workspace.MemoryDir, 0o755); err != nil {
		return Workspace{}, false, fmt.Errorf("create task memory directory: %w", err)
	}
	if err := m.save(workspace); err != nil {
		return Workspace{}, false, err
	}
	return workspace, true, nil
}

func (m *Manager) save(workspace Workspace) error {
	content, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task manifest: %w", err)
	}
	if err := os.MkdirAll(workspace.RootDir, 0o755); err != nil {
		return fmt.Errorf("create task root directory: %w", err)
	}
	// Batch4/L: atomic write — interrupted WriteFile left corrupt JSON that
	// List previously dropped silently (claude_code_main sessionStorage pattern).
	tmpPath := workspace.ManifestPath + ".tmp"
	payload := append(content, '\n')
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return fmt.Errorf("write task manifest temp: %w", err)
	}
	// Windows: Rename fails if destination exists — remove first after temp is ready.
	_ = os.Remove(workspace.ManifestPath)
	if err := os.Rename(tmpPath, workspace.ManifestPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("commit task manifest: %w", err)
	}
	return nil
}

func (m *Manager) workspaceShell(taskID string) Workspace {
	rootDir := filepath.Join(m.BaseDir, taskID)
	return Workspace{
		ID:           taskID,
		RootDir:      rootDir,
		SessionsDir:  filepath.Join(rootDir, "sessions"),
		MemoryDir:    filepath.Join(rootDir, "memory"),
		ManifestPath: filepath.Join(rootDir, manifestFileName),
	}
}

func (w Workspace) LatestTranscriptPath() string {
	if strings.TrimSpace(w.LatestTranscript) == "" {
		return ""
	}
	if filepath.IsAbs(w.LatestTranscript) {
		return w.LatestTranscript
	}
	return filepath.Join(w.RootDir, filepath.FromSlash(w.LatestTranscript))
}

func sanitizeTaskID(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	lastHyphen := false
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if r <= unicode.MaxASCII {
				builder.WriteRune(r)
				lastHyphen = false
			}
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return ""
	}
	if len(result) > 48 {
		result = result[:48]
	}
	return result
}

func deriveTaskID(title string) string {
	cleaned := stripLeakedCLIFlagsForSlug(title)
	slug := stripCLINoiseFromSlug(sanitizeTaskID(stripNegationClausesForSlug(cleaned)))
	hashSource := cleaned
	if strings.TrimSpace(hashSource) == "" {
		hashSource = title
	}
	hash := shortHash(hashSource)
	if slug == "" {
		return "task-" + hash
	}
	return fmt.Sprintf("%s-%s", slug, hash)
}

func stripLeakedCLIFlagsForSlug(title string) string {
	s := strings.TrimSpace(title)
	known := []string{"permission-mode", "new-task", "resume", "from-file", "continue-from-pause", "progress", "task"}
	for {
		if !strings.HasPrefix(s, "-") {
			break
		}
		body := strings.TrimLeft(s, "-")
		lower := strings.ToLower(body)
		matched := ""
		for _, k := range known {
			if lower == k || strings.HasPrefix(lower, k+" ") || strings.HasPrefix(lower, k+"\t") {
				matched = k
				break
			}
		}
		if matched == "" {
			break
		}
		rest := strings.TrimSpace(body[len(matched):])
		if matched == "new-task" {
			s = rest
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			s = ""
			break
		}
		s = strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	}
	return strings.TrimSpace(s)
}

func stripCLINoiseFromSlug(slug string) string {
	for {
		trimmed := slug
		for _, p := range []string{
			"permission-mode-", "permissionmode-",
			"acceptedits-", "accept-edits-",
			"new-task-", "newtask-",
			"dontask-", "dont-ask-",
		} {
			trimmed = strings.TrimPrefix(trimmed, p)
		}
		trimmed = strings.Trim(trimmed, "-")
		if trimmed == slug {
			return slug
		}
		slug = trimmed
	}
}

// stripNegationClausesForSlug removes "don't make X / 不要做成 X" enumerations so
// negative examples (HTTP, webhook, SQLite, …) do not pollute the task slug (E7).
// Language-agnostic: applies to any NL prompt, not just Go.
func stripNegationClausesForSlug(title string) string {
	text := title
	patterns := []string{
		`(?is)不要再?\s*bootstrap[^。\n]*`,
		`(?is)也不要再?\s*bootstrap[^。\n]*`,
		`(?is)不要(?:命令行)?脚手架[^。\n]*`,
		`(?is)don't\s+bootstrap[^.!\n]*`,
		`(?is)do\s+not\s+bootstrap[^.!\n]*`,
		`(?is)不要做成[^。\n]*`,
		`(?is)不要再做成[^。\n]*`,
		// F5′: "不要再做 CSV / Markdown HTTP / …" (list-style, not only 不要做成)
		`(?is)不要再做[^。\n；;]*`,
		`(?is)不要做[^。\n；;]*`,
		`(?is)那些旧题[^。\n]*`,
		// F5/r11: parenthetical "不是 CSV" and similar enumerations.
		`(?is)[（(][^）)]*不是[^）)]*[）)]`,
		`(?is)[（(][^）)]*不要再?[做成][^）)]*[）)]`,
		`(?is)不是\s*(网站|csv|sqlite|http|https|webhook|markdown|sse|rest|api|cli|bin|数据库|database|cidr|token-?bucket|crud)[^，,、。\n]*`,
		`(?is)也?不要\s*(网站|csv|sqlite|http|https|webhook|markdown|sse|rest|api|cli|bin|数据库|database|cidr|token-?bucket|crud)[^，,、。\n]*`,
		`(?is)do\s+not\s+(?:make|build|create|use|add|turn(?:\s+it)?\s+into|do)[^.!\n]*`,
		`(?is)don't\s+(?:make|build|create|use|add|turn(?:\s+it)?\s+into|do)[^.!\n]*`,
		`(?is)never\s+(?:make|build|create|use|add)[^.!\n]*`,
		`(?is)avoid\s+(?:making|building|creating|using)?[^.!\n]*`,
		`(?is)not\s+(?:a\s+)?(?:website|csv|sqlite|http|webhook|markdown|sse|rest|database|cli|bin|cidr|token-?bucket|crud)[^.!\n]*`,
		`(?is)(?:^|[,;，、])\s*no\s+(?:a\s+)?(?:cli|http|bin|webhook)\b[^.!\n]*`,
		`(?is)without\s+(?:a\s+)?(?:cli|http|bin)\b[^.!\n]*`,
		`(?is)不要用[^。\n；;]*`,
		`(?is)do\s+not\s+use\s+(?:wall|time\.sleep|sleep)[^.!\n]*`,
		`(?is)don't\s+use\s+(?:wall|time\.sleep|sleep)[^.!\n]*`,
		`(?is)no\s+wall-?clock\s+sleep[^.!\n]*`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

func shortHash(value string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(strings.TrimSpace(value)))
	return fmt.Sprintf("%08x", hasher.Sum32())
}
