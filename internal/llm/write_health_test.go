package llm

import "testing"

func TestRejectUnhealthyWrite_TruncatedGo(t *testing.T) {
	err := rejectUnhealthyWrite("auth.go", "package handlers\n\nfunc Register() {\n\tif len(req.Password) ")
	if err == nil {
		t.Fatal("expected rejection of truncated Go")
	}
}

func TestRejectUnhealthyWrite_ValidGo(t *testing.T) {
	src := "package main\n\nfunc main() {}\n"
	if err := rejectUnhealthyWrite("main.go", src); err != nil {
		t.Fatalf("valid Go rejected: %v", err)
	}
}

func TestRejectUnhealthyWrite_NonGoSkipped(t *testing.T) {
	if err := rejectUnhealthyWrite("readme.md", "incomplete"); err != nil {
		t.Fatalf("non-Go should skip: %v", err)
	}
}

func TestRejectUnhealthyWrite_HollowMain(t *testing.T) {
	if err := rejectUnhealthyWrite("cmd/server/main.go", "package main\n"); err == nil {
		t.Fatal("expected hollow package main rejection")
	}
	padded := "package main\n\nconst placeholder = 1\nconst more = 2\n"
	if err := rejectUnhealthyWrite("cmd/server/main.go", padded); err == nil {
		t.Fatal("expected hollow main without func main rejection")
	}
	emptyMain := "package main\n\nfunc main() {}\n"
	if err := rejectUnhealthyWrite("cmd/server/main.go", emptyMain); err == nil {
		t.Fatal("expected empty/unwired HTTP main rejection")
	}
	wired := "package main\n\nimport \"net/http\"\n\nfunc main() {\n\thttp.ListenAndServe(\":8080\", nil)\n}\n"
	if err := rejectUnhealthyWrite("cmd/server/main.go", wired); err != nil {
		t.Fatalf("wired HTTP main rejected: %v", err)
	}
}

func TestRejectUnhealthyWrite_HollowServerJS(t *testing.T) {
	if err := rejectUnhealthyWrite("src/server.js", "const x = 1;\n"); err == nil {
		t.Fatal("expected empty server.js rejection")
	}
}

func TestRejectUnhealthyWrite_TruncatedJS(t *testing.T) {
	// Mirrors mid_js_nl_p2_r2 notes.test.js cut mid Math.abs(...)
	src := `import test from 'node:test';
import assert from 'node:assert';

test('create note', async () => {
  const now = Date.now();
  const createdTime = Date.parse(note.created_at);
  assert.ok(Math.abs(now - createdTime
`
	if err := rejectUnhealthyWrite("test/notes.test.js", src); err == nil {
		t.Fatal("expected rejection of truncated JS with unbalanced parens")
	}
}

func TestRejectUnhealthyWrite_ValidJS(t *testing.T) {
	src := `export function add(a, b) {
  return a + b;
}
`
	if err := rejectUnhealthyWrite("src/math.js", src); err != nil {
		t.Fatalf("valid JS rejected: %v", err)
	}
}

func TestRejectUnhealthyWrite_PoisonTSStub(t *testing.T) {
	if err := rejectUnhealthyWrite("src/test/test.ts", "# test.ts — stub\n"); err == nil {
		t.Fatal("expected rejection of # poison stub in .ts")
	}
	if err := rejectUnhealthyWrite("src/types/sql.js", "// sql.js — stub\n"); err == nil {
		t.Fatal("expected rejection of junk sql.js path")
	}
}

func TestRejectUnhealthyWrite_CustomChecker(t *testing.T) {
	SetWriteHealthChecker(func(path, content string) error {
		if path == "blocked.js" {
			return errWriteBlocked
		}
		return nil
	})
	defer ClearWriteHealthChecker()
	if err := rejectUnhealthyWrite("blocked.js", "ok()"); err == nil {
		t.Fatal("expected custom checker rejection")
	}
	if err := rejectUnhealthyWrite("ok.js", "export const x = 1;\n"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRejectUnhealthyWrite_JSDocParensAreComplete(t *testing.T) {
	src := "export function add(data) {\n  // rate must be in (0, 1)\n  return data;\n}\n"
	if err := rejectUnhealthyWrite("bloom.js", src); err != nil {
		t.Fatalf("complete JS with doc parens rejected: %v", err)
	}
}

var errWriteBlocked = errString("blocked by test checker")

type errString string

func (e errString) Error() string { return string(e) }

