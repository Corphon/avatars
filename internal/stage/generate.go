package stage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"avatars/internal/llm"
)

// Kind selects the generation brief and system prompt.
type Kind string

const (
	KindCreative  Kind = "creative"
	KindProject   Kind = "project"
	KindRegPoints Kind = "regpoints"
	KindFile      Kind = "file"
)

// DefaultGalleryAddr is the creative gallery bind address.
// Operator dashboard (`avatars serve`) stays on 127.0.0.1:5000.
const DefaultGalleryAddr = "127.0.0.1:5100"

var stageIDPattern = regexp.MustCompile(`^stage-[a-f0-9]{12}$`)

// GenerateOpts controls one create/overwrite/revise pass.
type GenerateOpts struct {
	Input     string // core content / idea (identity seed)
	Kind      Kind
	Direction string // --prompt or revise instruction
	EditID    string // existing id or "latest"
	ForceNew  bool   // ignore stable id; always mint a new card
	Model     string
}

func (o GenerateOpts) kind() Kind {
	if strings.TrimSpace(string(o.Kind)) == "" {
		return KindCreative
	}
	return o.Kind
}

func validStageID(id string) bool {
	return stageIDPattern.MatchString(strings.TrimSpace(id))
}

func stableStageID(kind Kind, seed string) string {
	sum := sha256.Sum256([]byte(string(kind) + "\n" + strings.TrimSpace(seed)))
	return "stage-" + hex.EncodeToString(sum[:])[:12]
}

// Generate creates a creative stage from free-form input (stable id, overwrite).
func (m *Module) Generate(ctx context.Context, input string) (*Stage, error) {
	return m.GenerateWith(ctx, GenerateOpts{Input: input, Kind: KindCreative})
}

// GenerateWith creates or revises a stage according to opts.
func (m *Module) GenerateWith(ctx context.Context, opts GenerateOpts) (*Stage, error) {
	if m.config.Client == nil {
		return nil, fmt.Errorf("stage: no LLM client configured")
	}
	opts.Kind = opts.kind()
	opts.Input = strings.TrimSpace(opts.Input)
	opts.Direction = strings.TrimSpace(opts.Direction)
	opts.EditID = strings.TrimSpace(opts.EditID)

	var existing *Stage
	if opts.EditID != "" {
		stg, err := m.resolveStage(opts.EditID)
		if err != nil {
			return nil, err
		}
		existing = stg
		if opts.Input == "" {
			opts.Input = stg.Input
		}
		if opts.Kind == KindCreative && strings.TrimSpace(stg.Kind) != "" {
			opts.Kind = Kind(stg.Kind)
		}
	}

	id := ""
	createdAt := time.Now().UTC()
	if existing != nil {
		id = existing.ID
		createdAt = existing.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
	} else if opts.ForceNew || opts.Input == "" {
		sum := sha256.Sum256([]byte(opts.Input + time.Now().Format(time.RFC3339Nano)))
		id = "stage-" + hex.EncodeToString(sum[:])[:12]
	} else {
		id = stableStageID(opts.Kind, opts.Input)
		if prev, err := m.Get(id); err == nil && prev != nil {
			existing = prev
			createdAt = prev.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
		}
	}
	if opts.Input == "" && existing == nil {
		return nil, fmt.Errorf("stage: input is empty")
	}

	var currentHTML string
	revising := existing != nil && (opts.EditID != "" || opts.Direction != "")
	if revising {
		raw, err := os.ReadFile(filepath.Join(m.config.StageDir, existing.Path))
		if err != nil {
			return nil, fmt.Errorf("read existing stage: %w", err)
		}
		currentHTML = string(raw)
	}

	brief := ""
	if !revising {
		brief, _ = m.generateBrief(ctx, opts)
	}

	content, err := m.generateStageHTML(ctx, opts, brief, currentHTML)
	if err != nil {
		return nil, fmt.Errorf("generate stage HTML: %w", err)
	}

	title := extractTitle(content, firstNonEmpty(opts.Direction, opts.Input))
	filename := id + ".html"
	filePath := filepath.Join(m.config.StageDir, filename)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write stage file: %w", err)
	}

	descSrc := opts.Input
	if opts.Direction != "" {
		descSrc = opts.Direction + " — " + opts.Input
	}
	stg := &Stage{
		ID:          id,
		Title:       title,
		Description: summarize(descSrc, 120),
		Input:       opts.Input,
		Path:        filename,
		CreatedAt:   createdAt,
		UpdatedAt:   time.Now().UTC(),
		Kind:        string(opts.Kind),
		Direction:   opts.Direction,
		Brief:       brief,
	}
	if err := m.saveStageMeta(stg); err != nil {
		return nil, fmt.Errorf("save stage meta: %w", err)
	}
	return stg, nil
}

func (m *Module) Get(id string) (*Stage, error) {
	id = strings.TrimSpace(id)
	if !validStageID(id) {
		return nil, fmt.Errorf("stage: invalid id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(m.config.StageDir, id+".json"))
	if err != nil {
		return nil, err
	}
	var stg Stage
	if err := json.Unmarshal(data, &stg); err != nil {
		return nil, err
	}
	if stg.ID == "" {
		stg.ID = id
	}
	return &stg, nil
}

func (m *Module) Latest() (*Stage, error) {
	list, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("stage: gallery is empty")
	}
	return &list[0], nil
}

func (m *Module) resolveStage(idOrLatest string) (*Stage, error) {
	if strings.EqualFold(idOrLatest, "latest") {
		return m.Latest()
	}
	return m.Get(idOrLatest)
}

// Resolve looks up a stage by id or the keyword "latest".
func (m *Module) Resolve(idOrLatest string) (*Stage, error) {
	return m.resolveStage(idOrLatest)
}

func (m *Module) Delete(id string) error {
	stg, err := m.Get(id)
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.config.StageDir, stg.Path))
	if err := os.Remove(filepath.Join(m.config.StageDir, stg.ID+".json")); err != nil {
		return err
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (m *Module) generateBrief(ctx context.Context, opts GenerateOpts) (string, error) {
	system := `You plan one visual HTML stage. Return compact JSON only:
{"title":"short title","form":"art|narrative|explorer|diagram|dashboard","must_show":["2-6 concrete elements"],"avoid":["stale or irrelevant things"],"notes":"one sentence art direction"}
No markdown. form must match the input kind.`
	user := "Kind: " + string(opts.kind()) + "\n"
	if opts.Direction != "" {
		user += "Direction: " + opts.Direction + "\n"
	}
	user += "Input:\n" + truncateStageInput(opts.Input, 8000)
	resp, err := m.config.Client.Generate(ctx, llm.Request{
		SystemPrompt:     system,
		UserPrompt:       user,
		Category:         llm.CategoryAnalysis,
		Model:            opts.Model,
		StructuredOutput: true,
		ThinkMode:        llm.ThinkModeOff,
		MaxTokens:        600,
		RequestTimeout:   2 * time.Minute,
	})
	if err != nil || resp.Fallback {
		return "", err
	}
	raw := strings.TrimSpace(resp.Text)
	if idx := strings.Index(raw, "{"); idx >= 0 {
		raw = raw[idx:]
		if end := strings.LastIndex(raw, "}"); end >= 0 {
			raw = raw[:end+1]
		}
	}
	var probe map[string]any
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return "", fmt.Errorf("brief is not JSON")
	}
	return raw, nil
}

func (m *Module) generateStageHTML(ctx context.Context, opts GenerateOpts, brief, currentHTML string) (string, error) {
	systemPrompt := stageHTMLSystemPrompt(opts.kind(), currentHTML != "")
	var user strings.Builder
	user.WriteString("Kind: ")
	user.WriteString(string(opts.kind()))
	user.WriteString("\n")
	if opts.Direction != "" {
		user.WriteString("User direction:\n")
		user.WriteString(opts.Direction)
		user.WriteString("\n\n")
	}
	if brief != "" {
		user.WriteString("Approved brief (follow this):\n")
		user.WriteString(brief)
		user.WriteString("\n\n")
	}
	if currentHTML != "" {
		user.WriteString("Revise this existing page. Output a complete replacement HTML file.\n")
		user.WriteString("Current HTML:\n")
		user.WriteString(truncateStageInput(currentHTML, 120000))
		user.WriteString("\n\n")
	}
	user.WriteString("Source input:\n")
	user.WriteString(truncateStageInput(opts.Input, 24000))

	req := llm.Request{
		SystemPrompt:     systemPrompt,
		UserPrompt:       user.String(),
		Category:         llm.CategoryCodeGeneration,
		Model:            opts.Model,
		ThinkMode:        llm.ThinkModeOff,
		RequestTimeout:   10 * time.Minute,
	}
	resp, err := m.generateStageHTMLOnce(ctx, req)
	if retryableStageHTMLFailure(err, resp) {
		fmt.Fprintf(os.Stderr, "stage HTML failed (%v); retrying once with the same prompt\n", err)
		resp, err = m.generateStageHTMLOnce(ctx, req)
	}
	if err != nil || resp.Fallback {
		if resp.Fallback && err == nil {
			return "", fmt.Errorf("LLM generation failed: provider returned fallback response (check API key/model)")
		}
		if resp.Fallback {
			return "", fmt.Errorf("LLM generation failed (fallback): %w", err)
		}
		return "", fmt.Errorf("LLM generation failed: %w", err)
	}
	content := normalizeStageHTML(resp.Text)
	if content == "" {
		return "", fmt.Errorf("LLM returned empty content")
	}
	if len(content) > 250000 {
		fmt.Fprintf(os.Stderr, "warning: stage HTML is %.1f KB (over 200KB target)\n", float64(len(content))/1024.0)
	}
	return content, nil
}

func (m *Module) generateStageHTMLOnce(ctx context.Context, req llm.Request) (llm.Response, error) {
	prog := newStageGenerateProgress(os.Stderr)
	defer prog.Close()
	req.StreamCallback = prog.OnContent
	req.ThinkingCallback = prog.OnThinking
	return m.config.Client.Generate(ctx, req)
}

func retryableStageHTMLFailure(err error, resp llm.Response) bool {
	if resp.Fallback {
		return false
	}
	if err != nil {
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, "empty response") ||
			strings.Contains(msg, "forcibly closed") ||
			strings.Contains(msg, "connection reset") ||
			strings.Contains(msg, "eof") ||
			strings.Contains(msg, "i/o timeout") ||
			strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "wsarecv")
	}
	return strings.TrimSpace(normalizeStageHTML(resp.Text)) == ""
}

func stageHTMLSystemPrompt(kind Kind, revising bool) string {
	var b strings.Builder
	b.WriteString("You are a creative director for avatars Stage. Output ONE complete HTML file. No markdown fences, no commentary outside the HTML.\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Inline CSS and JS only. Optional P5.js via CDN. Must open as a local file.\n")
	b.WriteString("- Dark theme unless the direction says otherwise. Keep under 200KB.\n")
	b.WriteString("- Include DOCTYPE, html, head, title, body. Subtle 'Generated by avatars' attribution.\n")
	if revising {
		b.WriteString("- This is a REVISION: apply the user's direction; keep what still works.\n")
	}
	switch kind {
	case KindProject:
		b.WriteString("- This is a PROJECT MAP, not source code delivery.\n")
		b.WriteString("- Trust the workflow plan / Success Criteria and the file tree over architecture.md (architecture may be stale).\n")
		b.WriteString("- Do not treat cache, __pycache__, egg-info, dist, build, or avatars test logs as first-class modules.\n")
		b.WriteString("- Interactive explorer: tree + layers/entry points + a short legend of what is confirmed vs inferred.\n")
	case KindRegPoints:
		b.WriteString("- Visualize multi-file change patterns as an interactive graph or flow. Group files per registration point.\n")
		b.WriteString("- Do not invent files that are not in the input.\n")
	case KindFile:
		b.WriteString("- Illustrate or diagram the provided file. Do not turn it into a full product.\n")
	default:
		b.WriteString("- Pure creative visualization of the idea. Do NOT inject project architecture, source trees, or repo conventions unless they appear in the input.\n")
		b.WriteString("- Story → narrative/illustration; data → chart; vague words → ambient art.\n")
	}
	return b.String()
}

func normalizeStageHTML(raw string) string {
	content := strings.TrimSpace(raw)
	if idx := strings.Index(content, "<!DOCTYPE html>"); idx >= 0 {
		content = content[idx:]
	} else if idx := strings.Index(content, "<!doctype html>"); idx >= 0 {
		content = content[idx:]
	} else if idx := strings.Index(content, "<html"); idx >= 0 {
		content = content[idx:]
	} else if idx := strings.Index(content, "```html"); idx >= 0 {
		content = content[idx+7:]
		if end := strings.LastIndex(content, "```"); end >= 0 {
			content = content[:end]
		}
	} else if idx := strings.Index(content, "```"); idx >= 0 {
		content = content[idx+3:]
		if end := strings.LastIndex(content, "```"); end >= 0 {
			content = content[:end]
		}
	}
	content = strings.TrimSpace(content)
	if end := strings.Index(strings.ToLower(content), "</html>"); end >= 0 {
		content = content[:end+len("</html>")]
	}
	return strings.TrimSpace(content)
}

func truncateStageInput(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n…(truncated)\n"
}
