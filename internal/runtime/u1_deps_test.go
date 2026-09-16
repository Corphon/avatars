package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNpmDepsNeedInstall_MissingTypesNode(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "linkly",
  "dependencies": { "express": "^4.0.0", "sql.js": "^1.0.0" },
  "devDependencies": { "typescript": "^5.0.0", "@types/node": "^22.0.0", "tsx": "^4.0.0" }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{"compilerOptions":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if !npmDepsNeedInstall(dir) {
		t.Fatal("expected install when node_modules missing")
	}
	// Partial tree: node_modules exists but @types/node missing.
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "typescript"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "typescript", "package.json"), []byte(`{"name":"typescript"}`), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "express"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "express", "package.json"), []byte(`{"name":"express"}`), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "sql.js"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "sql.js", "package.json"), []byte(`{"name":"sql.js"}`), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "tsx"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "tsx", "package.json"), []byte(`{"name":"tsx"}`), 0644)
	if !npmDepsNeedInstall(dir) {
		t.Fatal("expected install when @types/node declared but missing")
	}
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "@types", "node"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "@types", "node", "package.json"), []byte(`{"name":"@types/node"}`), 0644)
	if npmDepsNeedInstall(dir) {
		t.Fatal("expected no install when typescript + @types/node + key deps present")
	}
}

func TestLooksLikeMissingPackageDeps_TS2591(t *testing.T) {
	msg := `tsc --noEmit failed: src/__tests__/routes.test.ts(1,45): error TS2591: Cannot find name 'node:test'. Do you need to install type definitions for node? Try ` + "`npm i --save-dev @types/node`"
	if !looksLikeMissingPackageDeps(msg) {
		t.Fatal("expected missing-deps classification for TS2591")
	}
	if !looksLikePersistentMissingTypes(msg) {
		t.Fatal("expected persistent missing-types after install signal")
	}
	if looksLikeMissingPackageDeps("tsc failed: Argument of type 'string | string[]' is not assignable") {
		t.Fatal("app type errors must NOT classify as missing package deps")
	}
}

func TestHealthFailureFingerprint_MissingPackageDeps(t *testing.T) {
	a := healthFailureFingerprint(`tsc --noEmit failed: error TS2591: Cannot find name 'node:test'`)
	b := healthFailureFingerprint(`Try npm i --save-dev @types/node and then add 'node' to the types field`)
	if a != "missing_package_deps" || b != "missing_package_deps" {
		t.Fatalf("got %q %q", a, b)
	}
}

func TestEnsureProjectDependencies_NoOpEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := ensureProjectDependencies(dir); err != "" {
		t.Fatalf("empty dir should no-op, got %q", err)
	}
}

func TestProjectCompileCheck_CallsEnsureBeforeTscShape(t *testing.T) {
	// Smoke: go.mod-only project without sources should not panic.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644)
	// May fail build (no packages) — just ensure ensure path runs without panic.
	_ = projectCompileCheck(dir)
}

func TestLooksLikeMissingPackageDeps_Python(t *testing.T) {
	if looksLikeMissingPackageDeps("pytest failed: ModuleNotFoundError: No module named 'fastapi'") {
		// ok
	} else {
		t.Fatal("expected fastapi missing-deps")
	}
	if looksLikeMissingPackageDeps("pytest failed: ModuleNotFoundError: No module named 'app'") {
		t.Fatal("local app module must not trigger missing-deps")
	}
}

func TestNpmDepsNeedInstall_EmptyNodeModules(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","dependencies":{"express":"1"}}`), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)
	if !npmDepsNeedInstall(dir) {
		t.Fatal("empty node_modules should need install")
	}
}

func TestForceNpmInstallOnce_NoPackageJSON(t *testing.T) {
	if msg := forceNpmInstallOnce(t.TempDir()); msg != "" {
		t.Fatalf("expected empty, got %q", msg)
	}
}

func TestLooksLikePersistentMissingTypes_NotAppError(t *testing.T) {
	if looksLikePersistentMissingTypes("npm test failed: Database not initialized") {
		t.Fatal("app runtime error must not be persistent missing types")
	}
	if !strings.Contains(strings.ToLower("TS2591"), "ts2591") {
		t.Fatal("sanity")
	}
}
