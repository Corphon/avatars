package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// dropModulePackageConflicts removes package-dir writes that would shadow a
// sibling module file in the same batch (C4). Language-agnostic shapes:
//   - Python: foo.py vs foo/__init__.py
//   - JS/TS:  foo.js|foo.ts vs foo/index.js|index.ts
func dropModulePackageConflicts(files []builderCodeFile) (kept []builderCodeFile, dropped []string) {
	moduleStems := map[string]bool{}
	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		dir := filepath.ToSlash(filepath.Dir(p))
		base := filepath.Base(p)
		ext := strings.ToLower(filepath.Ext(base))
		stem := strings.TrimSuffix(strings.ToLower(base), ext)
		switch ext {
		case ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx":
			if stem != "__init__" && stem != "index" {
				key := dir + "/" + stem
				if dir == "." || dir == "" {
					key = stem
				}
				moduleStems[key] = true
			}
		}
	}
	for _, f := range files {
		p := filepath.ToSlash(f.Path)
		dir := filepath.ToSlash(filepath.Dir(p))
		base := strings.ToLower(filepath.Base(p))
		parent := filepath.ToSlash(filepath.Dir(dir))
		pkg := strings.ToLower(filepath.Base(dir))
		isPkgInit := false
		switch base {
		case "__init__.py":
			isPkgInit = true
		case "index.js", "index.mjs", "index.cjs", "index.ts", "index.tsx", "index.jsx":
			isPkgInit = true
		}
		if isPkgInit && pkg != "" && pkg != "." {
			key := parent + "/" + pkg
			if parent == "." || parent == "" {
				key = pkg
			}
			shadow := moduleStems[key]
			if !shadow {
				for _, ext := range []string{".py", ".js", ".ts", ".tsx", ".mjs", ".jsx"} {
					sib := filepath.Join(filepath.FromSlash(parent), pkg+ext)
					if parent == "." || parent == "" {
						sib = pkg + ext
					}
					if fileExists(sib) {
						shadow = true
						break
					}
				}
			}
			if shadow {
				dropped = append(dropped, p+" (module/package shadow — prefer sibling module)")
				continue
			}
		}
		kept = append(kept, f)
	}
	return kept, dropped
}

// resolveModulePackageShadowsOnDisk deletes empty/stub package dirs that
// shadow a sibling module file (C4 cleanup after writes).
func resolveModulePackageShadowsOnDisk(wd string) []string {
	var removed []string
	_ = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".avatars", "venv", ".venv", "node_modules", "__pycache__", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(wd, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		base := strings.ToLower(filepath.Base(rel))
		dir := filepath.ToSlash(filepath.Dir(rel))
		parent := filepath.ToSlash(filepath.Dir(dir))
		pkg := filepath.Base(dir)

		isInit := base == "__init__.py" ||
			base == "index.js" || base == "index.ts" || base == "index.tsx" ||
			base == "index.mjs" || base == "index.jsx"
		if !isInit || pkg == "." || pkg == "" {
			return nil
		}
		var sib string
		switch {
		case strings.HasSuffix(base, ".py"):
			sib = filepath.Join(wd, filepath.FromSlash(parent), pkg+".py")
		default:
			for _, ext := range []string{".js", ".ts", ".tsx", ".mjs"} {
				cand := filepath.Join(wd, filepath.FromSlash(parent), pkg+ext)
				if fileExists(cand) {
					sib = cand
					break
				}
			}
		}
		if sib == "" || !fileExists(sib) {
			return nil
		}
		data, _ := os.ReadFile(path)
		trimmed := strings.TrimSpace(string(data))
		if len(trimmed) > 80 && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "//") {
			return nil
		}
		_ = os.Remove(path)
		removed = append(removed, rel)
		pkgDir := filepath.Dir(path)
		if entries, e := os.ReadDir(pkgDir); e == nil && len(entries) == 0 {
			_ = os.Remove(pkgDir)
		}
		return nil
	})
	return removed
}
