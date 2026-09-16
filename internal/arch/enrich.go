package arch

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"avatars/internal/llm"
)

const (
	archEnrichTimeout = 45 * time.Second
	archOverviewMax   = 600 // runes — refuse LLM overviews that are still dumps
)

// EnrichResult summarizes optional LLM enrichment (R3-9C).
type EnrichResult struct {
	Attempted bool
	Applied   bool
	Reason    string
}

// NeedsLLMEnrich reports whether Overview/RegPoints still need semantic lift.
func NeedsLLMEnrich(doc *ArchDoc) bool {
	if doc == nil {
		return true
	}
	ov := strings.TrimSpace(doc.Overview)
	if ov == "" || OverviewLooksLikePrompt(ov) || strings.Contains(ov, "prompt omitted") ||
		strings.Contains(ov, "auto-generated from scan") || strings.Contains(ov, "auto-updated after build") {
		return true
	}
	return !hasUsefulRegPoints(doc)
}

// EnrichArchWithLLM optionally asks the LLM for Overview + Registration Points.
//
// Risk controls:
//   - skipped when NeedsLLMEnrich is false
//   - short timeout; failure leaves doc unchanged (caller keeps heuristic/scan data)
//   - validateArchDocPaths drops hallucinated paths
//   - Overview hard-capped; prompt-like LLM text rejected
//   - never overwrites scan-derived DataFlow / filtered External deps
func EnrichArchWithLLM(ctx context.Context, client llm.Client, root string, doc *ArchDoc, scan *ProjectScan) EnrichResult {
	out := EnrichResult{}
	if client == nil || doc == nil {
		out.Reason = "no llm client or nil doc"
		return out
	}
	if !NeedsLLMEnrich(doc) {
		out.Reason = "already enriched enough"
		return out
	}
	if scan == nil {
		var err error
		scan, err = ScanProject(root)
		if err != nil || scan == nil {
			out.Reason = "scan failed"
			return out
		}
	}
	if scan.FileStats.TotalFiles == 0 {
		out.Reason = "empty project — skip llm enrich"
		return out
	}

	out.Attempted = true
	ectx, cancel := context.WithTimeout(ctx, archEnrichTimeout)
	defer cancel()

	focus := "HTTP/API routes, middleware chains, CLI commands, and other multi-file registration patterns"
	generated, err := GenerateArchDoc(ectx, client, scan, "analyze", "", doc, focus)
	if err != nil {
		out.Reason = "llm failed: " + err.Error()
		return out
	}
	if generated == nil {
		out.Reason = "llm returned nil"
		return out
	}

	applied := false
	if ov := strings.TrimSpace(generated.Overview); ov != "" && !OverviewLooksLikePrompt(ov) {
		if utf8.RuneCountInString(ov) > archOverviewMax {
			runes := []rune(ov)
			ov = string(runes[:archOverviewMax]) + "…"
		}
		doc.Overview = ov
		applied = true
	}

	if len(generated.RegPoints) > 0 {
		doc.RegPoints = MergeRegPoints(generated.RegPoints, doc.RegPoints)
		if hasUsefulRegPoints(doc) {
			applied = true
		}
	}

	// Prefer LLM layer descriptions when present; keep scan paths if LLM empty.
	if len(generated.Layers) > 0 {
		doc.Layers = mergeLayersPreferLLM(doc.Layers, generated.Layers)
		applied = true
	}
	if len(generated.Conventions) > 0 && len(doc.Conventions) == 0 {
		doc.Conventions = generated.Conventions
		applied = true
	}
	// Entry points: keep scan-inferred kinds when possible; fill gaps from LLM.
	if len(generated.EntryPoints) > 0 {
		doc.EntryPoints = mergeEntryPoints(doc.EntryPoints, generated.EntryPoints)
	}

	out.Applied = applied
	if applied {
		out.Reason = "overview/regpoints enriched"
		doc.Meta.Source = "workflow-auto-enrich"
	} else {
		out.Reason = "llm output unused (failed quality checks)"
	}
	return out
}

func mergeLayersPreferLLM(scanLayers, llmLayers []Layer) []Layer {
	if len(llmLayers) == 0 {
		return scanLayers
	}
	// LLM descriptions are usually better; keep if paths look non-empty.
	var out []Layer
	for _, l := range llmLayers {
		if strings.TrimSpace(l.Name) == "" {
			continue
		}
		if len(l.Paths) == 0 {
			// Try attach paths from scan layer with same name.
			for _, s := range scanLayers {
				if strings.EqualFold(s.Name, l.Name) || strings.Contains(s.Name, l.Name) {
					l.Paths = s.Paths
					break
				}
			}
		}
		if strings.TrimSpace(l.Description) == "" {
			l.Description = describeLayer(l.Name)
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return scanLayers
	}
	return out
}

func mergeEntryPoints(scanEPs, llmEPs []EntryPoint) []EntryPoint {
	if len(scanEPs) == 0 {
		return llmEPs
	}
	byPath := map[string]EntryPoint{}
	for _, ep := range scanEPs {
		byPath[ep.Path] = ep
	}
	for _, ep := range llmEPs {
		if existing, ok := byPath[ep.Path]; ok {
			// Prefer content-inferred http-server over llm "cli" when already http.
			if existing.Kind == "http-server" {
				if existing.Summary == "" || existing.Summary == "Entry point" {
					existing.Summary = ep.Summary
					byPath[ep.Path] = existing
				}
				continue
			}
			if ep.Kind != "" {
				existing.Kind = ep.Kind
			}
			if ep.Summary != "" {
				existing.Summary = ep.Summary
			}
			byPath[ep.Path] = existing
			continue
		}
		byPath[ep.Path] = ep
	}
	var out []EntryPoint
	for _, ep := range byPath {
		out = append(out, ep)
	}
	return out
}

// FormatEnrichLog is a short log line for runtime emit/debug.
func FormatEnrichLog(r EnrichResult) string {
	if !r.Attempted {
		return fmt.Sprintf("skipped (%s)", r.Reason)
	}
	if r.Applied {
		return fmt.Sprintf("applied (%s)", r.Reason)
	}
	return fmt.Sprintf("attempted-not-applied (%s)", r.Reason)
}
