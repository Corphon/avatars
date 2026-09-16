package stage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avatars/internal/llm"
)

type stubStageLLM struct {
	calls []llm.Request
	brief string
	html  string
}

func (s *stubStageLLM) Provider() string             { return "stub" }
func (s *stubStageLLM) Supports(llm.Capability) bool { return false }
func (s *stubStageLLM) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	s.calls = append(s.calls, req)
	if req.StructuredOutput {
		text := s.brief
		if text == "" {
			text = `{"title":"Sky","form":"art","must_show":["stars"],"avoid":["repo tree"],"notes":"ambient"}`
		}
		return llm.Response{Text: text}, nil
	}
	html := s.html
	if html == "" {
		html = "<!DOCTYPE html><html><head><title>Sky</title></head><body>art</body></html>"
	}
	if strings.Contains(req.UserPrompt, "User direction:") {
		html = "<!DOCTYPE html><html><head><title>Pixel Sky</title></head><body>pixel</body></html>"
	}
	return llm.Response{Text: html}, nil
}

func TestStableStageID_SameSeedSameID(t *testing.T) {
	a := stableStageID(KindCreative, "starry sky")
	b := stableStageID(KindCreative, "starry sky")
	c := stableStageID(KindProject, "starry sky")
	if a != b {
		t.Fatalf("expected stable id, got %s vs %s", a, b)
	}
	if a == c {
		t.Fatal("kind must affect id")
	}
	if !validStageID(a) {
		t.Fatalf("invalid id %s", a)
	}
}

func TestGenerateWith_OverwritesSameIdeaAndSkipsArch(t *testing.T) {
	dir := t.TempDir()
	stub := &stubStageLLM{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := mod.GenerateWith(ctx, GenerateOpts{Input: "starry sky", Kind: KindCreative})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mod.GenerateWith(ctx, GenerateOpts{Input: "starry sky", Kind: KindCreative})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("overwrite should keep id %s vs %s", first.ID, second.ID)
	}
	htmlCall := false
	for _, call := range stub.calls {
		if call.StructuredOutput {
			continue
		}
		htmlCall = true
		if strings.Contains(call.UserPrompt, "architecture.md") {
			t.Fatalf("creative generate leaked architecture.md into user prompt")
		}
		if !strings.Contains(strings.ToLower(call.SystemPrompt), "do not inject project architecture") {
			t.Fatalf("creative system prompt missing arch guard: %s", call.SystemPrompt)
		}
		if call.ThinkMode != llm.ThinkModeOff {
			t.Fatalf("HTML generate ThinkMode=%q want Off (prefix-cache safe request field)", call.ThinkMode)
		}
	}
	if !htmlCall {
		t.Fatal("expected HTML generation call")
	}
}

func TestGenerateWith_EditKeepsID(t *testing.T) {
	dir := t.TempDir()
	stub := &stubStageLLM{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := mod.GenerateWith(ctx, GenerateOpts{Input: "starry sky"})
	if err != nil {
		t.Fatal(err)
	}
	revised, err := mod.GenerateWith(ctx, GenerateOpts{EditID: first.ID, Direction: "pixel art"})
	if err != nil {
		t.Fatal(err)
	}
	if revised.ID != first.ID {
		t.Fatalf("edit changed id %s -> %s", first.ID, revised.ID)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stage", revised.Path))
	if !strings.Contains(string(raw), "pixel") {
		t.Fatalf("revised html=%s", raw)
	}
}

func TestDeleteAndNormalizeHTML(t *testing.T) {
	dir := t.TempDir()
	stub := &stubStageLLM{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	stg, err := mod.GenerateWith(context.Background(), GenerateOpts{Input: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mod.Delete(stg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.Get(stg.ID); err == nil {
		t.Fatal("expected missing after delete")
	}
	got := normalizeStageHTML("intro\n```html\n<!DOCTYPE html><html>ok</html>\n```\nmore")
	if got != "<!DOCTYPE html><html>ok</html>" {
		t.Fatalf("normalize=%q", got)
	}
}

func TestProjectPromptTrustsPlan(t *testing.T) {
	dir := t.TempDir()
	stub := &stubStageLLM{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mod.GenerateWith(context.Background(), GenerateOpts{Input: "tree\nmain.go", Kind: KindProject})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range stub.calls {
		if call.StructuredOutput {
			continue
		}
		found = true
		if !strings.Contains(call.SystemPrompt, "Trust the workflow plan") {
			t.Fatalf("project prompt=%s", call.SystemPrompt)
		}
	}
	if !found {
		t.Fatal("missing html call")
	}
}

func TestGalleryDeleteAPI(t *testing.T) {
	dir := t.TempDir()
	stub := &stubStageLLM{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	stg, err := mod.GenerateWith(context.Background(), GenerateOpts{Input: "sky"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mod.RegisterHTTP(mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/stage/"+stg.ID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type flakyEmptyThenHTML struct {
	htmlCalls int
}

func (s *flakyEmptyThenHTML) Provider() string             { return "stub" }
func (s *flakyEmptyThenHTML) Supports(llm.Capability) bool { return false }
func (s *flakyEmptyThenHTML) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.StructuredOutput {
		return llm.Response{Text: `{"title":"Sky","form":"art","must_show":["stars"],"avoid":[],"notes":"x"}`}, nil
	}
	s.htmlCalls++
	if s.htmlCalls == 1 {
		return llm.Response{Text: ""}, nil
	}
	return llm.Response{Text: "<!DOCTYPE html><html><head><title>Sky</title></head><body>ok</body></html>"}, nil
}

func TestGenerateWith_RetriesEmptyHTML(t *testing.T) {
	dir := t.TempDir()
	stub := &flakyEmptyThenHTML{}
	mod, err := New(Config{StageDir: filepath.Join(dir, "stage"), WebDir: filepath.Join(dir, "web"), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	stg, err := mod.GenerateWith(context.Background(), GenerateOpts{Input: "starry sky", Kind: KindCreative})
	if err != nil {
		t.Fatal(err)
	}
	if stub.htmlCalls != 2 {
		t.Fatalf("html calls=%d want 2 (empty then retry)", stub.htmlCalls)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stage", stg.Path))
	if !strings.Contains(string(raw), "<body>ok</body>") {
		t.Fatalf("html=%s", raw)
	}
}

