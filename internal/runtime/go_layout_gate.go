package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

// detectConflictingGoLayout reports Go package-layout smells that compile
// individually but leave a hollow/split module (F34):
//   - root .go declaring package P while a subdirectory also holds package P
//   - hollow internal/doc (or similar) beside a real library package that
//     already has package docs / implementation
//
// Returns "" when the layout is coherent or there is no go.mod.
func detectConflictingGoLayout(wd string) string {
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err != nil {
		return ""
	}

	rootPkgs := goPackageNamesInDir(wd, false)
	if len(rootPkgs) > 0 {
		for _, sub := range listImmediateSubdirs(wd) {
			base := filepath.Base(sub)
			if base == "cmd" || base == "vendor" || base == "testdata" {
				continue
			}
			subPkgs := goPackageNamesInDir(sub, true)
			for pkg := range rootPkgs {
				if pkg == "main" {
					continue
				}
				if subPkgs[pkg] {
					return "conflicting Go layout: root package " + pkg +
						" alongside subdirectory " + filepath.ToSlash(strings.TrimPrefix(sub, wd+string(filepath.Separator))) +
						" also declaring package " + pkg +
						" — move root .go sources into " + base + "/ (keep one package tree; prefer file-header docs, not a root package shell)"
				}
			}
		}
	}

	// Hollow docs package under internal/doc while a sibling library package exists.
	internalDoc := filepath.Join(wd, "internal", "doc")
	if dirHasGoSources(internalDoc) {
		if lib := findSiblingLibraryPackage(wd); lib != "" {
			return "conflicting Go layout: hollow internal/doc package beside library " +
				lib + "/ — document " + lib + " in *.go file headers; remove hollow internal/doc/"
		}
	}

	// F38/F42: duplicate library — top-level lib/ AND internal/lib/ both have impl.
	if mod := goModuleDirNameAt(wd); mod != "" {
		top := filepath.Join(wd, mod)
		buried := filepath.Join(wd, "internal", mod)
		if dirHasImplGoSources(top) && dirHasImplGoSources(buried) {
			return "conflicting Go layout: both " + mod + "/ and internal/" + mod +
				"/ contain implementation — keep a single public package at " + mod + "/ and remove internal/" + mod + "/"
		}
	}
	// F38: public library only under internal/<mod>/ with no top-level package.
	if c := resolveLayoutCharter(wd); c.ForbidInternalLib && c.LibraryDir != "" {
		buried := filepath.Join(wd, "internal", c.LibraryDir)
		top := filepath.Join(wd, c.LibraryDir)
		if dirHasImplGoSources(buried) && !dirHasImplGoSources(top) {
			return "conflicting Go layout: public library API buried under internal/" +
				c.LibraryDir + "/ — move to " + c.LibraryDir + "/ so importers can use it"
		}
	}

	// Public package files living under internal/<other>/ (not a genuine helper).
	if msg := detectMisplacedPublicGoUnderInternal(wd); msg != "" {
		return msg
	}

	return ""
}

func listImmediateSubdirs(wd string) []string {
	entries, err := os.ReadDir(wd)
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		out = append(out, filepath.Join(wd, name))
	}
	return out
}

// goPackageNamesInDir returns package clause names for .go files directly in dir.
// When recursive is true, only the immediate directory is still scanned (one level);
// recursive is reserved for future nested scans and currently ignored beyond the dir itself.
func goPackageNamesInDir(dir string, _ bool) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if pkg := parseGoPackageClause(string(data)); pkg != "" {
			out[pkg] = true
		}
	}
	return out
}

func parseGoPackageClause(src string) string {
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "/*") {
			continue
		}
		if strings.HasPrefix(trim, "package ") {
			pkg := strings.TrimSpace(strings.TrimPrefix(trim, "package"))
			pkg = strings.Fields(pkg)[0]
			pkg = strings.TrimSuffix(pkg, ";")
			return pkg
		}
		// Stop at first non-comment code that isn't package (shouldn't happen).
		if !strings.HasPrefix(trim, "/*") {
			break
		}
	}
	return ""
}

func detectMisplacedPublicGoUnderInternal(wd string) string {
	mod := goModuleDirNameAt(wd)
	if mod == "" {
		return ""
	}
	for _, rel := range misplacedPublicGoUnderInternal(wd, mod) {
		return "conflicting Go layout: public package " + mod +
			" file under " + rel + " — lift it to the public package location; keep genuine internal/<helper>/ packages"
	}
	return ""
}

func misplacedPublicGoUnderInternal(wd, mod string) []string {
	internal := filepath.Join(wd, "internal")
	ents, err := os.ReadDir(internal)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.EqualFold(name, mod) || strings.EqualFold(name, "doc") || strings.EqualFold(name, "debug") {
			continue
		}
		sub := filepath.Join(internal, name)
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sub, f.Name()))
			if err != nil {
				continue
			}
			pkg := strings.TrimSuffix(parseGoPackageClause(string(data)), "_test")
			if !strings.EqualFold(pkg, mod) {
				continue
			}
			out = append(out, filepath.ToSlash(filepath.Join("internal", name, f.Name())))
		}
	}
	return out
}

func liftMisplacedPublicGoSources(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	mod := goModuleDirNameAt(wd)
	if mod == "" {
		return nil
	}
	var lifted []string
	for _, rel := range misplacedPublicGoUnderInternal(wd, mod) {
		src := filepath.Join(wd, filepath.FromSlash(rel))
		base := filepath.Base(rel)
		destRel := publicPackageDestAt(wd, base, base)
		dest := filepath.Join(wd, filepath.FromSlash(destRel))
		if filepath.Clean(src) == filepath.Clean(dest) {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			if err := os.Remove(src); err == nil {
				lifted = append(lifted, rel+" (duplicate of "+destRel+")")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			continue
		}
		if err := os.Rename(src, dest); err == nil {
			lifted = append(lifted, rel+" → "+destRel)
		}
	}
	return lifted
}

func findSiblingLibraryPackage(wd string) string {
	if mod := goModuleDirNameAt(wd); mod != "" {
		cand := filepath.Join(wd, mod)
		if dirHasImplGoSources(cand) {
			return mod
		}
	}
	for _, sub := range listImmediateSubdirs(wd) {
		base := filepath.Base(sub)
		if base == "cmd" || base == "internal" || base == "docs" || base == "tmp" {
			continue
		}
		if dirHasImplGoSources(sub) {
			return base
		}
	}
	return ""
}

// purgeHollowGoDocPackages removes internal/doc when a real library package
// already exists (F34 continue-run pollution from sanitize inventing internal/doc).
func purgeHollowGoDocPackages(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	lib := findSiblingLibraryPackage(wd)
	if lib == "" {
		return nil
	}
	docDir := filepath.Join(wd, "internal", "doc")
	if !dirHasGoSources(docDir) {
		return nil
	}
	entries, err := os.ReadDir(docDir)
	if err != nil {
		return nil
	}
	var deleted []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".go") {
			continue
		}
		full := filepath.Join(docDir, name)
		if err := os.Remove(full); err == nil {
			deleted = append(deleted, filepath.ToSlash(filepath.Join("internal", "doc", name))+
				" (hollow docs; library is "+lib+"/)")
		}
	}
	// Best-effort remove empty dir.
	_ = os.Remove(docDir)
	_ = os.Remove(filepath.Join(wd, "internal")) // only if empty
	return deleted
}

func goModuleDirNameAt(wd string) string {
	data, err := os.ReadFile(filepath.Join(wd, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		mod = strings.Trim(mod, `"'`)
		if mod == "" {
			return ""
		}
		return filepath.Base(filepath.FromSlash(mod))
	}
	return ""
}

// purgeDuplicateBuriedLibrary deletes internal/<lib>/*.go when top-level <lib>/
// already holds the public implementation (F38/F42 Critic cleanup).
func purgeDuplicateBuriedLibrary(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	mod := goModuleDirNameAt(wd)
	if mod == "" {
		return nil
	}
	top := filepath.Join(wd, mod)
	buried := filepath.Join(wd, "internal", mod)
	if !dirHasImplGoSources(top) || !dirHasGoSources(buried) {
		return nil
	}
	entries, err := os.ReadDir(buried)
	if err != nil {
		return nil
	}
	var deleted []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".go") {
			continue
		}
		full := filepath.Join(buried, name)
		if err := os.Remove(full); err == nil {
			deleted = append(deleted, filepath.ToSlash(filepath.Join("internal", mod, name))+
				" (duplicate of "+mod+"/)")
		}
	}
	_ = os.Remove(buried)
	return deleted
}

// purgeRootConflictingGoPackage removes root-level .go files whose package name
// collides with a real subdirectory library package (F55). Typical pollution:
// root doc.go (package urlcanon) beside urlcanon/*.go. Prefer keeping the
// subdirectory tree; root shells are deleted so health can continue.
func purgeRootConflictingGoPackage(wd string) []string {
	if isAvatarsHarnessTree(wd) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err != nil {
		return nil
	}
	rootPkgs := goPackageNamesInDir(wd, false)
	if len(rootPkgs) == 0 {
		return nil
	}
	conflictPkgs := map[string]string{} // pkg → subdir base
	for _, sub := range listImmediateSubdirs(wd) {
		base := filepath.Base(sub)
		switch base {
		case "cmd", "vendor", "testdata", "internal", "docs", "tmp":
			continue
		}
		if !dirHasGoSources(sub) {
			continue
		}
		subPkgs := goPackageNamesInDir(sub, true)
		for pkg := range rootPkgs {
			if pkg == "main" {
				continue
			}
			if subPkgs[pkg] {
				conflictPkgs[pkg] = base
			}
		}
	}
	if len(conflictPkgs) == 0 {
		return nil
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		return nil
	}
	var deleted []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".go") {
			continue
		}
		full := filepath.Join(wd, name)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		pkg := parseGoPackageClause(string(data))
		sub, ok := conflictPkgs[pkg]
		if !ok {
			continue
		}
		if err := os.Remove(full); err != nil {
			continue
		}
		deleted = append(deleted, name+" (root package "+pkg+" conflicts with "+sub+"/)")
	}
	return deleted
}

// healConflictingGoLayout attempts automatic cleanup of known layout smells
// (F55). Returns whether the tree is clean after purge, plus deleted paths.
func healConflictingGoLayout(wd string) (clean bool, deleted []string) {
	if isAvatarsHarnessTree(wd) {
		return true, nil
	}
	deleted = append(deleted, liftMisplacedPublicGoSources(wd)...)
	if detectConflictingGoLayout(wd) == "" {
		return true, deleted
	}
	deleted = append(deleted, purgeRootConflictingGoPackage(wd)...)
	deleted = append(deleted, purgeMisplacedRootDuplicates(wd)...)
	deleted = append(deleted, purgeHollowGoDocPackages(wd)...)
	deleted = append(deleted, purgeDuplicateBuriedLibrary(wd)...)
	return detectConflictingGoLayout(wd) == "", deleted
}
