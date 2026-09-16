package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	surgicalMinSimilarity = 0.55 // original lines that must still appear in proposed
	surgicalMaxHunkLines  = 40   // refuse oversized single replace/insert
	surgicalMaxOps        = 24   // hard cap on mutations per dump
	surgicalMaxFileLines  = 4000 // skip LCS on huge files
)

// SurgicalApplyResult summarizes dump→hunk application (language-agnostic).
type SurgicalApplyResult struct {
	Inserts  int
	Replaces int
	Skipped  int
	Reason   string
}

func (r SurgicalApplyResult) Applied() int {
	return r.Inserts + r.Replaces
}

// SurgicalApplyFromDump converts a full-file LLM dump into insert + replace
// hunks and applies them surgically. Never full-overwrites the target.
//
// Safety gates:
//   - proposed must preserve enough of the original (line similarity)
//   - each old block for replace must appear exactly once
//   - hunk size / op count caps
func SurgicalApplyFromDump(originalContent, proposedContent, filePath, workingDir string) (SurgicalApplyResult, error) {
	var out SurgicalApplyResult
	originalContent = strings.ReplaceAll(originalContent, "\r\n", "\n")
	proposedContent = strings.ReplaceAll(proposedContent, "\r\n", "\n")
	if strings.TrimSpace(proposedContent) == "" {
		out.Reason = "empty proposed content"
		return out, nil
	}
	if strings.TrimSpace(originalContent) == "" {
		out.Reason = "empty original — use write_file for new files"
		return out, nil
	}
	if strings.TrimSpace(originalContent) == strings.TrimSpace(proposedContent) {
		out.Reason = "proposed identical to original"
		return out, nil
	}

	origLines := strings.Split(originalContent, "\n")
	propLines := strings.Split(proposedContent, "\n")
	if len(origLines) > surgicalMaxFileLines || len(propLines) > surgicalMaxFileLines {
		out.Reason = "file too large for surgical dump apply"
		return out, nil
	}

	sim := lineSetRecall(origLines, propLines)
	if sim < surgicalMinSimilarity {
		out.Reason = fmt.Sprintf("similarity %.2f < %.2f — refuse dump (too different for surgical apply)", sim, surgicalMinSimilarity)
		return out, nil
	}

	hunks := computeLineHunks(origLines, propLines)
	if len(hunks) == 0 {
		n, err := DiffAndApplyEdits(originalContent, proposedContent, filePath, workingDir)
		out.Inserts = n
		if err != nil {
			return out, err
		}
		if n == 0 {
			out.Reason = "no hunks extracted"
		}
		return out, nil
	}

	ops := 0
	for _, h := range hunks {
		if ops >= surgicalMaxOps {
			out.Skipped++
			out.Reason = "op cap reached"
			continue
		}
		switch h.Kind {
		case hunkInsert:
			if len(h.NewLines) == 0 || len(h.NewLines) > surgicalMaxHunkLines {
				out.Skipped++
				continue
			}
			if strings.TrimSpace(h.Anchor) == "" {
				out.Skipped++
				continue
			}
			content := strings.Join(h.NewLines, "\n")
			_, err := PreciseEditTool{}.Call(context.Background(), PreciseEditInput{
				FilePath:   filePath,
				Anchor:     h.Anchor,
				Content:    content,
				Position:   "after",
				IfMissing:  true,
				WorkingDir: workingDir,
			})
			if err != nil {
				out.Skipped++
				continue
			}
			out.Inserts++
			ops++
		case hunkReplace:
			if len(h.OldLines) == 0 || len(h.OldLines) > surgicalMaxHunkLines || len(h.NewLines) > surgicalMaxHunkLines {
				out.Skipped++
				continue
			}
			oldBlock := strings.Join(h.OldLines, "\n")
			newBlock := strings.Join(h.NewLines, "\n")
			if strings.TrimSpace(oldBlock) == strings.TrimSpace(newBlock) {
				continue
			}
			if err := applyUniqueReplace(filePath, workingDir, oldBlock, newBlock); err != nil {
				out.Skipped++
				continue
			}
			out.Replaces++
			ops++
		case hunkDelete:
			out.Skipped++
		}
	}

	if out.Applied() == 0 && out.Reason == "" {
		out.Reason = "no safe hunks applied"
	}
	return out, nil
}

type hunkKind int

const (
	hunkInsert hunkKind = iota
	hunkReplace
	hunkDelete
)

type lineHunk struct {
	Kind     hunkKind
	Anchor   string
	OldLines []string
	NewLines []string
}

// lineSetRecall: fraction of non-empty original lines that appear in proposed (set).
func lineSetRecall(orig, prop []string) float64 {
	propSet := make(map[string]bool, len(prop))
	for _, l := range prop {
		t := strings.TrimSpace(l)
		if t != "" {
			propSet[t] = true
		}
	}
	total, hit := 0, 0
	for _, l := range orig {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		total++
		if propSet[t] {
			hit++
		}
	}
	if total == 0 {
		return 1
	}
	return float64(hit) / float64(total)
}

// computeLineHunks walks both sequences and emits insert/replace/delete hunks
// between common lines. Language-agnostic (exact line equality).
func computeLineHunks(a, b []string) []lineHunk {
	// Index first occurrence of each line in b for matching.
	bIndex := map[string][]int{}
	for j, line := range b {
		bIndex[line] = append(bIndex[line], j)
	}
	usedB := make([]bool, len(b))

	var matches [][2]int // (i, j) common lines in order
	jCursor := 0
	for i, line := range a {
		found := -1
		for _, j := range bIndex[line] {
			if j >= jCursor && !usedB[j] {
				found = j
				break
			}
		}
		if found >= 0 {
			matches = append(matches, [2]int{i, found})
			usedB[found] = true
			jCursor = found + 1
		}
	}

	var hunks []lineHunk
	ai, bj := 0, 0
	lastCommon := ""
	for _, m := range matches {
		mi, mj := m[0], m[1]
		oldBlock := a[ai:mi]
		newBlock := b[bj:mj]
		if len(oldBlock) == 0 && len(newBlock) > 0 {
			hunks = append(hunks, lineHunk{Kind: hunkInsert, Anchor: lastCommon, NewLines: append([]string{}, newBlock...)})
		} else if len(oldBlock) > 0 && len(newBlock) == 0 {
			hunks = append(hunks, lineHunk{Kind: hunkDelete, OldLines: append([]string{}, oldBlock...)})
		} else if len(oldBlock) > 0 && len(newBlock) > 0 {
			hunks = append(hunks, lineHunk{Kind: hunkReplace, OldLines: append([]string{}, oldBlock...), NewLines: append([]string{}, newBlock...)})
		}
		lastCommon = a[mi]
		ai, bj = mi+1, mj+1
	}
	// Trailing
	oldBlock := a[ai:]
	newBlock := b[bj:]
	if len(oldBlock) == 0 && len(newBlock) > 0 {
		hunks = append(hunks, lineHunk{Kind: hunkInsert, Anchor: lastCommon, NewLines: append([]string{}, newBlock...)})
	} else if len(oldBlock) > 0 && len(newBlock) == 0 {
		hunks = append(hunks, lineHunk{Kind: hunkDelete, OldLines: append([]string{}, oldBlock...)})
	} else if len(oldBlock) > 0 && len(newBlock) > 0 {
		hunks = append(hunks, lineHunk{Kind: hunkReplace, OldLines: append([]string{}, oldBlock...), NewLines: append([]string{}, newBlock...)})
	}
	return hunks
}

// applyUniqueReplace replaces oldBlock with newBlock when oldBlock appears exactly once.
func applyUniqueReplace(filePath, workingDir, oldBlock, newBlock string) error {
	workingDir, err := filepath.Abs(filepath.Clean(workingDir))
	if err != nil {
		return err
	}
	targetPath, err := resolveWriteTarget(workingDir, filePath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}
	body := string(data)
	norm := strings.ReplaceAll(body, "\r\n", "\n")
	oldBlock = strings.ReplaceAll(oldBlock, "\r\n", "\n")
	newBlock = strings.ReplaceAll(newBlock, "\r\n", "\n")
	count := strings.Count(norm, oldBlock)
	if count != 1 {
		return fmt.Errorf("old block not unique (count=%d)", count)
	}
	updated := strings.Replace(norm, oldBlock, newBlock, 1)
	if strings.Contains(body, "\r\n") {
		// Best-effort: if original had CRLF, rewrite with CRLF.
		crlfCount := strings.Count(body, "\r\n")
		lfOnly := strings.Count(strings.ReplaceAll(body, "\r\n", ""), "\n")
		if crlfCount > 0 && lfOnly == 0 {
			updated = strings.ReplaceAll(updated, "\n", "\r\n")
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(targetPath), filepath.Base(targetPath)+".avatars-surg-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write([]byte(updated)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if errMsg := validateFileAfterEdit(tmpPath, filePath); errMsg != "" {
		return fmt.Errorf("validation: %s", errMsg)
	}
	_ = os.Remove(targetPath)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return os.WriteFile(targetPath, []byte(updated), 0o644)
	}
	return nil
}
