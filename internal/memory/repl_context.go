package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ReplContext holds accumulated REPL session state.
type ReplContext struct {
	Files             []string `json:"files"`
	Actions           []string `json:"actions"`
	LastKind          string   `json:"last_kind"`
	LastVerifierError string   `json:"last_verifier_error,omitempty"` // E2: smart recovery
	ConversationSummary string `json:"conversation_summary,omitempty"` // C3: running summary
}

const (
	replProjectMemoryDir  = ".avatars/memory"
	replProjectMemoryPath = ".avatars/memory/hot-memory.db"
	maxReplFiles          = 20
	maxReplActions        = 5
)

// initReplContextTable ensures the repl_context table exists in the
// project-level memory DB. This is called lazily — the REPL path does
// not go through the full application bootstrap, so we open our own
// connection to the shared DB file.
func initReplContextTable() (*sql.DB, error) {
	if err := os.MkdirAll(replProjectMemoryDir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	db, err := sql.Open("sqlite", replProjectMemoryPath)
	if err != nil {
		return nil, fmt.Errorf("open project memory: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS repl_context (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL DEFAULT '{}',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT OR IGNORE INTO repl_context (id, data) VALUES (1, '{}');
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init repl_context table: %w", err)
	}
	return db, nil
}

var (
	replDB   *sql.DB
	replOnce sync.Once
)

func getReplDB() *sql.DB {
	replOnce.Do(func() {
		db, err := initReplContextTable()
		if err != nil {
			return
		}
		replDB = db
	})
	return replDB
}

// LoadReplContext reads the current REPL session context from the
// project-level memory DB (.avatars/memory/hot-memory.db).
func LoadReplContext() ReplContext {
	db := getReplDB()
	if db == nil {
		return ReplContext{}
	}
	var raw string
	if err := db.QueryRow("SELECT data FROM repl_context WHERE id = 1").Scan(&raw); err != nil {
		return ReplContext{}
	}
	var ctx ReplContext
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return ReplContext{}
	}
	return ctx
}

func saveReplContext(db *sql.DB, ctx ReplContext) error {
	data, err := json.Marshal(ctx)
	if err != nil {
		return fmt.Errorf("marshal repl context: %w", err)
	}
	_, err = db.Exec("UPDATE repl_context SET data = ?, updated_at = datetime('now') WHERE id = 1", string(data))
	return err
}

// SaveReplContext persists the REPL session context.
func SaveReplContext(ctx ReplContext) {
	db := getReplDB()
	if db == nil {
		return
	}
	_ = saveReplContext(db, ctx)
}

// UpdateReplContext is called after each REPL turn to accumulate state
// into the project-level memory DB. Files and actions are deduplicated
// and capped.
func UpdateReplContext(input string, kind string, artifacts []string) {
	ctx := LoadReplContext()

	seen := make(map[string]bool)
	var files []string
	for _, f := range artifacts {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	for _, f := range ctx.Files {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	if len(files) > maxReplFiles {
		files = files[:maxReplFiles]
	}
	ctx.Files = files

	summary := strings.TrimSpace(input)
	if len(summary) > 150 {
		summary = summary[:150] + "..."
	}
	actions := []string{summary}
	for _, a := range ctx.Actions {
		if len(actions) >= maxReplActions {
			break
		}
		actions = append(actions, a)
	}
	ctx.Actions = actions
	ctx.LastKind = kind

	// C3: Build a running natural language summary of the conversation.
	ctx.ConversationSummary = buildConversationSummary(ctx)

	SaveReplContext(ctx)
}

// detectFileDependencies scans the accumulated files for import/require
// statements and returns a summary of which files depend on which.
func detectFileDependencies(files []string) string {
	if len(files) < 2 {
		return ""
	}
	type dep struct{ importer, imported string }
	var deps []dep
	seen := make(map[string]bool)

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		text := string(content)
		base := filepath.Base(f)
		for _, other := range files {
			if other == f {
				continue
			}
			otherBase := filepath.Base(other)
			otherName := strings.TrimSuffix(otherBase, filepath.Ext(otherBase))
			if strings.Contains(text, otherName) || strings.Contains(text, "\""+otherBase+"\"") {
				key := f + "→" + other
				if !seen[key] {
					seen[key] = true
					deps = append(deps, dep{base, otherBase})
				}
			}
		}
	}

	if len(deps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, d := range deps {
		b.WriteString(fmt.Sprintf("  %s → imports %s\n", d.importer, d.imported))
	}
	return b.String()
}

// buildConversationSummary generates a natural language summary from the
// accumulated session context, giving the LLM a cohesive understanding of
// what the user is building.
func buildConversationSummary(ctx ReplContext) string {
	if len(ctx.Actions) == 0 {
		return ""
	}
	var b strings.Builder

	// Detect project type from files.
	hasGo := false
	hasPython := false
	hasHTML := false
	hasJS := false
	for _, f := range ctx.Files {
		switch {
		case strings.HasSuffix(f, ".go") || strings.Contains(f, "go.mod"):
			hasGo = true
		case strings.HasSuffix(f, ".py") || strings.Contains(f, "pyproject.toml"):
			hasPython = true
		case strings.HasSuffix(f, ".html"):
			hasHTML = true
		case strings.HasSuffix(f, ".js") || strings.Contains(f, "package.json"):
			hasJS = true
		}
	}

	// Write a cohesive summary.
	if hasGo {
		b.WriteString("User is building a Go project. ")
	} else if hasPython {
		b.WriteString("User is building a Python project. ")
	} else if hasHTML || hasJS {
		b.WriteString("User is creating web content. ")
	}

	if len(ctx.Actions) == 1 {
		b.WriteString("They just started. ")
	} else {
		b.WriteString(fmt.Sprintf("They have made %d requests so far. ", len(ctx.Actions)))
	}

	// Describe recent actions.
	if len(ctx.Actions) >= 2 {
		b.WriteString("Recent work: ")
		for i, a := range ctx.Actions {
			if i >= 3 {
				break
			}
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(a)
		}
		b.WriteString(". ")
	}

	// Mention active files.
	activeFiles := make([]string, 0, 3)
	for _, f := range ctx.Files {
		// Prefer source files over docs/config.
		if strings.HasSuffix(f, ".go") || strings.HasSuffix(f, ".py") || strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".html") {
			activeFiles = append(activeFiles, filepath.Base(f))
		}
	}
	if len(activeFiles) > 0 && len(activeFiles) <= 3 {
		b.WriteString("Active files: " + strings.Join(activeFiles, ", ") + ".")
	} else if len(activeFiles) > 3 {
		b.WriteString(fmt.Sprintf("Active files include %s and %d others.", activeFiles[0], len(activeFiles)-1))
	}

	return b.String()
}

// RecordVerifierError stores the last verifier error for smart recovery.
// When the next edit/script request comes in, the LLM sees this error
// and can attempt to fix it.
func RecordVerifierError(errText string) {
	ctx := LoadReplContext()
	ctx.LastVerifierError = strings.TrimSpace(errText)
	SaveReplContext(ctx)
}

// GetLastVerifierError returns the stored verifier error for smart recovery.
// Used by P37-7 self-repair loop.
func GetLastVerifierError() string {
	ctx := LoadReplContext()
	return ctx.LastVerifierError
}

// ClearVerifierError clears the stored verifier error after a successful fix.
func ClearVerifierError() {
	ctx := LoadReplContext()
	ctx.LastVerifierError = ""
	SaveReplContext(ctx)
}

// BuildReplContextForLLM returns a formatted string for injection into
// the LLM router's prompt, enabling multi-turn conversational continuity.
// Uses the shared project-level memory DB (.avatars/memory/hot-memory.db).
func BuildReplContextForLLM() string {
	ctx := LoadReplContext()
	if len(ctx.Files) == 0 && len(ctx.Actions) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n--- Session context (SQLite-backed, project memory DB) ---\n")
	if ctx.ConversationSummary != "" {
		b.WriteString("Summary: " + ctx.ConversationSummary + "\n\n")
	}
	if len(ctx.Files) > 0 {
		b.WriteString("Files in this session (MOST RECENT first):\n")
		for i, f := range ctx.Files {
			b.WriteString("  - " + filepath.ToSlash(f))
			if i == 0 {
				b.WriteString(" ← LAST CREATED/MODIFIED")
			}
			b.WriteString("\n")
		}
		b.WriteString("When the user says 'add/fix/change/modify' without naming a file, they mean the LAST file above.\n")
	}
	if len(ctx.Actions) > 0 {
		b.WriteString("\nRecent requests:\n")
		for i, a := range ctx.Actions {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, a))
		}
	}
	if ctx.LastVerifierError != "" {
		b.WriteString("\nLast verifier error (the user likely wants you to fix this):\n")
		errSummary := ctx.LastVerifierError
		if len(errSummary) > 500 {
			errSummary = errSummary[:500] + "..."
		}
		b.WriteString("  " + errSummary + "\n")
		b.WriteString("If the user says 'fix the error' or 'fix it', they mean this error. Use edit to fix the file.\n")
	}
	// C1: Detect file dependencies from the accumulated file list.
	if deps := detectFileDependencies(ctx.Files); deps != "" {
		b.WriteString("\nFile dependencies:\n" + deps)
	}

	b.WriteString("\nCONTINUOUS session — every request builds on previous work.\n")
	b.WriteString("Vague targets ('the code', 'that file') refer to files listed above.\n")
	b.WriteString("--- End session context ---\n")
	return b.String()
}
