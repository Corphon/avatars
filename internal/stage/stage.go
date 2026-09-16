// Package stage provides an LLM-driven creative engine that turns any input
// into a visual, interactive web experience. Inspired by Claude Code's Design
// System, the Stage module treats the LLM as the creative director: give it
// any content (text, story, project, code, data), and it generates a
// self-contained HTML page that best represents that content.
//
// The generated pages are hosted via avatars' built-in HTTP server and
// displayed in a card-based gallery. Each stage is a unique creative
// interpretation — no templates, no constraints, just LLM creativity.
package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"avatars/internal/llm"
)

// Stage represents one generated stage — a self-contained HTML page
// that visually represents some input content.
type Stage struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Input       string    `json:"input"`
	Path        string    `json:"path"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Direction   string    `json:"direction,omitempty"`
	Brief       string    `json:"brief,omitempty"`
	Thumbnail   string    `json:"thumbnail,omitempty"`
}

// Config holds the Stage module configuration.
type Config struct {
	StageDir   string // directory to store generated stages (default: "stage/")
	WebDir     string // web assets directory (default: "web/stage/")
	Client     llm.Client
	ProjectDir string
}

// Module is the Stage engine.
type Module struct {
	config Config
}

// New creates a new Stage module.
func New(config Config) (*Module, error) {
	if config.StageDir == "" {
		config.StageDir = "stage"
	}
	if config.WebDir == "" {
		config.WebDir = "web/stage"
	}
	if err := os.MkdirAll(config.StageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create stage dir: %w", err)
	}
	if err := os.MkdirAll(config.WebDir, 0o755); err != nil {
		return nil, fmt.Errorf("create web dir: %w", err)
	}
	return &Module{config: config}, nil
}

func stageSortTime(s Stage) time.Time {
	if !s.UpdatedAt.IsZero() {
		return s.UpdatedAt
	}
	return s.CreatedAt
}

func (m *Module) saveStageMeta(stg *Stage) error {
	data, err := json.MarshalIndent(stg, "", "  ")
	if err != nil {
		return err
	}
	metaPath := filepath.Join(m.config.StageDir, stg.ID+".json")
	return os.WriteFile(metaPath, append(data, '\n'), 0o644)
}

// List returns all generated stages, newest first.
func (m *Module) List() ([]Stage, error) {
	entries, err := os.ReadDir(m.config.StageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stages []Stage
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.config.StageDir, entry.Name()))
		if err != nil {
			continue
		}
		var stg Stage
		if err := json.Unmarshal(data, &stg); err != nil {
			continue
		}
		stages = append(stages, stg)
	}
	sort.Slice(stages, func(i, j int) bool {
		return stageSortTime(stages[i]).After(stageSortTime(stages[j]))
	})
	return stages, nil
}

// GalleryHTML generates an HTML gallery page showing all stages as cards.
// When relativeLinks is true (static/file:// export), card hrefs are relative
// filenames so double-clicking stage/index.html works on Windows.
func (m *Module) GalleryHTML() (string, error) {
	return m.galleryHTML(false)
}

func (m *Module) galleryHTML(relativeLinks bool) (string, error) {
	stages, err := m.List()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n")
	b.WriteString("<meta charset=\"UTF-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")
	b.WriteString("<title>avatars Stage Gallery</title>\n")
	b.WriteString("<style>\n")
	b.WriteString(galleryCSS)
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString("<header><h1>avatars Stage</h1><p>Visual sketches — not project source of truth</p>\n")
	if !relativeLinks {
		b.WriteString("<form id=\"create-form\" onsubmit=\"createStage(event)\" style=\"margin-top:1rem;display:flex;gap:0.5rem;justify-content:center;max-width:640px;margin-left:auto;margin-right:auto;flex-wrap:wrap\">\n")
		b.WriteString("<input type=\"text\" id=\"stage-input\" placeholder=\"Describe a sketch (same idea overwrites the same card)\" style=\"flex:1;min-width:220px;padding:0.5rem 1rem;border-radius:8px;border:1px solid #30363d;background:#0d1117;color:#c9d1d9;font-size:1rem\">\n")
		b.WriteString("<button type=\"submit\" style=\"padding:0.5rem 1.5rem;border-radius:8px;border:none;background:#238636;color:#fff;font-size:1rem;cursor:pointer;white-space:nowrap\">Create</button>\n")
		b.WriteString("</form>\n")
		b.WriteString("<div id=\"create-status\" style=\"text-align:center;margin-top:0.5rem;color:#8b949e;font-size:0.85rem\">Same prompt updates one card. CLI: avatars stage --edit latest \"direction\"</div>\n")
	} else {
		b.WriteString("<p style=\"text-align:center;color:#8b949e;font-size:0.9rem\">Static export — run <code>avatars stage --serve</code> (port 5100) for Create / Revise / Delete.</p>\n")
	}
	b.WriteString("</header>\n")
	if !relativeLinks {
		b.WriteString("<script>\n")
		b.WriteString("async function createStage(e){e.preventDefault();\n")
		b.WriteString("  const input=document.getElementById('stage-input');\n")
		b.WriteString("  const status=document.getElementById('create-status');\n")
		b.WriteString("  const desc=input.value.trim(); if(!desc) return;\n")
		b.WriteString("  input.disabled=true; let t=0; status.textContent='Generating… 0s (often 1–3 min)';\n")
		b.WriteString("  const iv=setInterval(()=>{t++; status.textContent='Generating… '+t+'s (keep this tab open)';},1000);\n")
		b.WriteString("  try {\n")
		b.WriteString("    const r=await fetch('/api/stage',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({description:desc})});\n")
		b.WriteString("    clearInterval(iv);\n")
		b.WriteString("    if(!r.ok){const x=await r.text();status.textContent='Error: '+x;}\n")
		b.WriteString("    else{const d=await r.json();status.textContent='Saved: '+d.title;input.value='';setTimeout(()=>location.reload(),800);}\n")
		b.WriteString("  }catch(err){clearInterval(iv);status.textContent='Error: '+err.message;}\n")
		b.WriteString("  input.disabled=false;\n")
		b.WriteString("}\n")
		b.WriteString("async function reviseStage(id){\n")
		b.WriteString("  const dir=prompt('Revision direction (keeps this card):'); if(!dir||!dir.trim()) return;\n")
		b.WriteString("  const status=document.getElementById('create-status');\n")
		b.WriteString("  status.textContent='Revising '+id+'…';\n")
		b.WriteString("  try{\n")
		b.WriteString("    const r=await fetch('/api/stage/'+id+'/revise',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({direction:dir.trim()})});\n")
		b.WriteString("    if(!r.ok){status.textContent='Error: '+await r.text();return;}\n")
		b.WriteString("    location.reload();\n")
		b.WriteString("  }catch(err){status.textContent='Error: '+err.message;}\n")
		b.WriteString("}\n")
		b.WriteString("async function deleteStage(id){\n")
		b.WriteString("  if(!confirm('Delete '+id+'?')) return;\n")
		b.WriteString("  const r=await fetch('/api/stage/'+id,{method:'DELETE'});\n")
		b.WriteString("  if(!r.ok){alert(await r.text());return;} location.reload();\n")
		b.WriteString("}\n")
		b.WriteString("</script>\n")
	}
	b.WriteString("<main class=\"gallery\">\n")

	if len(stages) == 0 {
		b.WriteString("<div class=\"empty\"><p>No stages yet. Create one with <code>avatars stage \"your idea\"</code></p></div>\n")
	}

	for _, stg := range stages {
		href := "/stage/" + stg.Path
		if relativeLinks {
			href = stg.Path // same directory as index.html
		}
		stamp := stg.CreatedAt
		if !stg.UpdatedAt.IsZero() {
			stamp = stg.UpdatedAt
		}
		kind := stg.Kind
		if kind == "" {
			kind = "creative"
		}
		b.WriteString("<article class=\"card\">\n")
		b.WriteString(fmt.Sprintf("<a href=\"%s\" target=\"_blank\">\n", href))
		b.WriteString(fmt.Sprintf("<div class=\"card-preview\"><iframe src=\"%s\" loading=\"lazy\" sandbox=\"allow-scripts allow-same-origin\"></iframe></div>\n", href))
		b.WriteString(fmt.Sprintf("<div class=\"card-body\"><h2>%s</h2><p>%s</p></div>\n", template.HTMLEscapeString(stg.Title), template.HTMLEscapeString(stg.Description)))
		b.WriteString("</a>\n")
		b.WriteString(fmt.Sprintf("<div class=\"card-meta\"><span>%s</span><time>%s</time></div>\n", template.HTMLEscapeString(kind), stamp.Format("2006-01-02 15:04")))
		if !relativeLinks {
			b.WriteString("<div class=\"card-actions\">")
			b.WriteString(fmt.Sprintf("<button type=\"button\" onclick=\"reviseStage('%s')\">Revise</button>", template.JSEscapeString(stg.ID)))
			b.WriteString(fmt.Sprintf("<button type=\"button\" class=\"danger\" onclick=\"deleteStage('%s')\">Delete</button>", template.JSEscapeString(stg.ID)))
			b.WriteString("</div>\n")
		}
		b.WriteString("</article>\n")
	}
	b.WriteString("</main>\n")
	b.WriteString("<footer><p>Generated by <strong>avatars</strong> — AI-powered creative engine</p></footer>\n")
	b.WriteString("</body>\n</html>")
	return b.String(), nil
}

// RegisterHTTP registers Stage HTTP handlers on the given mux.
// Serves: GET / (gallery), GET /stage/{file} (individual stages)
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	// Gallery page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html, err := m.GalleryHTML()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	// S2: API endpoint for in-browser stage creation.
	mux.HandleFunc("/api/stage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Description string `json:"description"`
			Kind        string `json:"kind"`
			Direction   string `json:"direction"`
			ForceNew    bool   `json:"force_new"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Description) == "" {
			http.Error(w, "Invalid request: description required", http.StatusBadRequest)
			return
		}
		if m.config.Client == nil {
			http.Error(w, "LLM client not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
		defer cancel()
		stg, err := m.GenerateWith(ctx, GenerateOpts{
			Input:     strings.TrimSpace(req.Description),
			Kind:      Kind(strings.TrimSpace(req.Kind)),
			Direction: strings.TrimSpace(req.Direction),
			ForceNew:  req.ForceNew,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("Generation failed: %v", err), http.StatusInternalServerError)
			return
		}
		_ = m.GenerateStatic()
		writeStageJSON(w, stg)
	})

	mux.HandleFunc("/api/stage/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/stage/"), "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if !validStageID(id) {
			http.Error(w, "invalid stage id", http.StatusBadRequest)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if err := m.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			_ = m.GenerateStatic()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(parts) == 2 && parts[1] == "revise" && r.Method == http.MethodPost {
			if m.config.Client == nil {
				http.Error(w, "LLM client not configured", http.StatusServiceUnavailable)
				return
			}
			var req struct {
				Direction string `json:"direction"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Direction) == "" {
				http.Error(w, "direction required", http.StatusBadRequest)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
			defer cancel()
			stg, err := m.GenerateWith(ctx, GenerateOpts{EditID: id, Direction: strings.TrimSpace(req.Direction)})
			if err != nil {
				http.Error(w, fmt.Sprintf("revise failed: %v", err), http.StatusInternalServerError)
				return
			}
			_ = m.GenerateStatic()
			writeStageJSON(w, stg)
			return
		}
		http.NotFound(w, r)
	})

	// Stage files
	stageFS := http.FileServer(http.Dir(m.config.StageDir))
	mux.Handle("/stage/", http.StripPrefix("/stage/", stageFS))

	// Web assets
	webFS := http.FileServer(http.Dir(m.config.WebDir))
	mux.Handle("/web/", http.StripPrefix("/web/", webFS))
}

// ServeStaticFiles writes the web assets to disk so the stage module
// can work standalone with just the directory.
func (m *Module) ServeStaticFiles() error {
	// Create gallery index
	indexHTML, err := m.GalleryHTML()
	if err != nil {
		return err
	}
	indexPath := filepath.Join(m.config.WebDir, "index.html")
	if err := os.WriteFile(indexPath, []byte(indexHTML), 0o644); err != nil {
		return err
	}
	// Write CSS
	cssPath := filepath.Join(m.config.WebDir, "stage.css")
	if _, err := os.Stat(cssPath); os.IsNotExist(err) {
		if err := os.WriteFile(cssPath, []byte(galleryCSS), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const galleryCSS = `
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
  background: #0d1117; color: #c9d1d9; min-height: 100vh;
}
header {
  text-align: center; padding: 3rem 1rem 2rem;
  background: linear-gradient(180deg, #161b22 0%, #0d1117 100%);
  border-bottom: 1px solid #21262d;
}
header h1 { font-size: 2.5rem; font-weight: 700; color: #58a6ff; }
header p { margin-top: 0.5rem; color: #8b949e; font-size: 1.1rem; }
.gallery {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 1.5rem; padding: 2rem; max-width: 1400px; margin: 0 auto;
}
.card {
  background: #161b22; border: 1px solid #21262d; border-radius: 12px;
  overflow: hidden; transition: transform 0.2s, box-shadow 0.2s;
}
.card:hover { transform: translateY(-4px); box-shadow: 0 8px 30px rgba(0,0,0,0.4); }
.card a { text-decoration: none; color: inherit; display: block; }
.card-preview { width: 100%; height: 220px; overflow: hidden; background: #0d1117; }
.card-preview iframe {
  width: 200%; height: 200%; border: none;
  transform: scale(0.5); transform-origin: 0 0;
  pointer-events: none;
}
.card-body { padding: 1rem; }
.card-body h2 { font-size: 1.1rem; color: #f0f6fc; margin-bottom: 0.3rem; }
.card-body p { font-size: 0.85rem; color: #8b949e; line-height: 1.4; }
.card-meta { padding: 0 1rem 0.5rem; font-size: 0.75rem; color: #484f58; display:flex; justify-content:space-between; gap:0.5rem; }
.card-actions { display:flex; gap:0.5rem; padding: 0 1rem 0.85rem; }
.card-actions button { flex:1; padding:0.35rem 0.5rem; border-radius:6px; border:1px solid #30363d; background:#21262d; color:#c9d1d9; cursor:pointer; font-size:0.8rem; }
.card-actions button.danger { color:#f85149; }
.empty { text-align: center; padding: 4rem; color: #8b949e; grid-column: 1 / -1; }
footer { text-align: center; padding: 2rem; color: #484f58; font-size: 0.85rem; border-top: 1px solid #21262d; margin-top: 2rem; }
code { background: #21262d; padding: 0.15em 0.4em; border-radius: 4px; font-size: 0.9em; }
`

// Helper functions

func extractTitle(html string, fallback string) string {
	// Try to extract <title> from the generated HTML.
	if start := strings.Index(html, "<title>"); start >= 0 {
		if end := strings.Index(html[start:], "</title>"); end >= 0 {
			title := strings.TrimSpace(html[start+7 : start+end])
			if title != "" {
				return title
			}
		}
	}
	// Use first meaningful line of input as title.
	lines := strings.SplitN(strings.TrimSpace(fallback), "\n", 2)
	title := strings.TrimSpace(lines[0])
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60]) + "..."
	}
	if title == "" {
		title = "Untitled Stage"
	}
	return title
}

func summarize(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "No description"
	}
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen-3]) + "..."
}

// GenerateStatic writes a static gallery to disk (for use without running server).
func (m *Module) GenerateStatic() error {
	// Relative links so file:// open works on Windows.
	indexHTML, err := m.galleryHTML(true)
	if err != nil {
		return err
	}
	indexPath := filepath.Join(m.config.StageDir, "index.html")
	if err := os.WriteFile(indexPath, []byte(indexHTML), 0o644); err != nil {
		return err
	}
	return m.ServeStaticFiles()
}

// WriteDefaultAssets creates the default web assets on disk.
func WriteDefaultAssets(webDir string) error {
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		return err
	}
	indexPath := filepath.Join(webDir, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		// Write a minimal redirect page
		html := `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta http-equiv="refresh" content="0;url=/"><title>avatars Stage</title></head><body><p>Redirecting to <a href="/">gallery</a>...</p></body></html>`
		if err := os.WriteFile(indexPath, []byte(html), 0o644); err != nil {
			return err
		}
	}
	cssPath := filepath.Join(webDir, "stage.css")
	if _, err := os.Stat(cssPath); os.IsNotExist(err) {
		if err := os.WriteFile(cssPath, []byte(galleryCSS), 0o644); err != nil {
			return err
		}
	}
	// Create stage directory
	stageDir := filepath.Join(filepath.Dir(webDir), "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}
	return nil
}

func writeStageJSON(w http.ResponseWriter, stg *Stage) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":    stg.ID,
		"title": stg.Title,
		"path":  stg.Path,
		"kind":  stg.Kind,
		"url":   "/stage/" + stg.Path,
	})
}

// LoadCLIStage generates a stage from CLI args.
func LoadCLIStage(client llm.Client, opts GenerateOpts) (*Stage, error) {
	m, err := New(Config{
		Client:     client,
		ProjectDir: ".",
	})
	if err != nil {
		return nil, err
	}
	stg, err := m.GenerateWith(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	if err := m.GenerateStatic(); err != nil {
		return nil, fmt.Errorf("generate static: %w", err)
	}
	return stg, nil
}
