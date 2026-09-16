package workflow

import (
	"os"
	"path/filepath"
	"strings"
)

// criteriaRequiresAuthEvidence is true when a Success Criteria line claims
// API-key / auth / 401 behavior (cross-lang wording).
func criteriaRequiresAuthEvidence(lowerDesc string) bool {
	lower := strings.ToLower(lowerDesc)
	needles := []string{
		"api-key", "api key", "api_key", "x-api-key", "鉴权", "401",
		"unauthenticated", "unauthorized", "bearer", "auth middleware",
		"authentication", "authenticate",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// criteriaRequiresDeleteEvidence is true when criteria claim an HTTP DELETE endpoint.
func criteriaRequiresDeleteEvidence(lowerDesc string) bool {
	lower := strings.ToLower(lowerDesc)
	if strings.Contains(lower, "delete /") || strings.Contains(lower, "method delete") {
		return true
	}
	if strings.Contains(lower, "delete") &&
		(strings.Contains(lower, "endpoint") || strings.Contains(lower, "接口") ||
			strings.Contains(lower, "cascade") || strings.Contains(lower, "204") ||
			strings.Contains(lower, "handler") || strings.Contains(lower, "/shipments") ||
			strings.Contains(lower, "/notes") || strings.Contains(lower, "/expenses") ||
			strings.Contains(lower, "/links") || strings.Contains(lower, "/targets")) {
		return true
	}
	return false
}

// criteriaRequiresAuthOrDeleteEvidence covers X1/Y3-style phase-2 claims.
func criteriaRequiresAuthOrDeleteEvidence(lowerDesc string) bool {
	return criteriaRequiresAuthEvidence(lowerDesc) || criteriaRequiresDeleteEvidence(lowerDesc)
}

// CriteriaAuthDeleteEvidenceOK reports whether disk sources satisfy auth/DELETE
// claims in lowerDesc (language-agnostic token scan).
func CriteriaAuthDeleteEvidenceOK(projectRoot, lowerDesc string) bool {
	needAuth := criteriaRequiresAuthEvidence(lowerDesc)
	needDel := criteriaRequiresDeleteEvidence(lowerDesc)
	if !needAuth && !needDel {
		return true
	}
	corpus := collectProjectSourceCorpus(projectRoot)
	if corpus == "" {
		return false
	}
	if needAuth && !projectHasAuthGate(corpus) {
		return false
	}
	if needDel && !projectHasDeleteEndpoint(corpus) {
		return false
	}
	return true
}

func collectProjectSourceCorpus(projectRoot string) string {
	var b strings.Builder
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, ".") || strings.Contains(slash, "node_modules/") ||
			strings.Contains(slash, "docs/workflow/") || strings.Contains(slash, "avatars/") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".rs", ".java", ".kt":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 200_000 {
			return nil
		}
		b.Write(data)
		b.WriteByte('\n')
		return nil
	})
	return strings.ToLower(b.String())
}

func projectHasAuthGate(codeLower string) bool {
	signals := []string{
		"apikeymiddleware", "x-api-key", "api_key", "apikey",
		"authorization", "bearer ", "requireauth", "requires_auth",
		"login_required", "httpbearer", "httpmiddleware",
		"securityschem", "depends(", "canactivate", "use(auth",
		"authenticate", "auth_middleware", "authmiddleware",
		"layer(auth", "from_fn(auth", "middleware::",
		"header.get(\"x-api-key\")", "header.get('x-api-key')",
		"r.header.get(\"x-api-key\")",
	}
	for _, s := range signals {
		if strings.Contains(codeLower, s) {
			return true
		}
	}
	return false
}

func projectHasDeleteEndpoint(codeLower string) bool {
	signals := []string{
		"methoddelete", "http.methoddelete", "http::method::delete",
		".delete(", "router.delete", "app.delete", "@app.delete",
		`methods=["delete"]`, `methods=['delete']`,
		"deleteshipment", "deletesnippet", "delete_handler", "deletehandler",
		"case http.methoddelete", "http.methoddelete",
	}
	for _, s := range signals {
		if strings.Contains(codeLower, s) {
			return true
		}
	}
	return false
}

// UnmarkSuccessCriteriaMissingAuthDeleteEvidence clears [x] on criteria that
// claim auth/DELETE but lack disk evidence (X1 honesty repair).
func UnmarkSuccessCriteriaMissingAuthDeleteEvidence(projectRoot string) int {
	planPath := filepath.Join(projectRoot, DocPaths["plan"])
	data, err := os.ReadFile(planPath)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	inSuccess := false
	changed := 0
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") {
			inSuccess = strings.Contains(trim, "Success Criteria")
			continue
		}
		if !inSuccess || !strings.HasPrefix(trim, "- [x]") {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "- [x]")))
		if !criteriaRequiresAuthOrDeleteEvidence(lower) {
			continue
		}
		if CriteriaAuthDeleteEvidenceOK(projectRoot, lower) {
			continue
		}
		lines[i] = strings.Replace(line, "- [x]", "- [ ]", 1)
		changed++
	}
	if changed == 0 {
		return 0
	}
	_ = WriteFileAtomic(planPath, []byte(strings.Join(lines, "\n")), 0644)
	return changed
}

// PlanHasUncheckedAuthDeleteCriteria is true when any Success Criteria that
// requires auth/DELETE evidence is still [ ] OR was falsely [x] (rechecked).
func PlanHasOpenAuthDeleteCriteria(projectRoot string) bool {
	planContent, err := ReadWorkflowDoc(projectRoot, "plan")
	if err != nil {
		return false
	}
	inSuccess := false
	for _, line := range strings.Split(planContent, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") {
			inSuccess = strings.Contains(trim, "Success Criteria")
			continue
		}
		if !inSuccess {
			continue
		}
		if strings.HasPrefix(trim, "- [ ]") {
			lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "- [ ]")))
			if criteriaRequiresAuthOrDeleteEvidence(lower) {
				return true
			}
		}
		if strings.HasPrefix(trim, "- [x]") {
			lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "- [x]")))
			if criteriaRequiresAuthOrDeleteEvidence(lower) && !CriteriaAuthDeleteEvidenceOK(projectRoot, lower) {
				return true
			}
		}
	}
	return false
}

// CriteriaRequiresAuthOrDeleteEvidence is the exported gate for runtime sync (X1).
func CriteriaRequiresAuthOrDeleteEvidence(lowerDesc string) bool {
	return criteriaRequiresAuthOrDeleteEvidence(lowerDesc)
}
