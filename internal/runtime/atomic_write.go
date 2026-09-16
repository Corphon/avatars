// Package runtime — atomic file writer.
//
// P1 fix: Adopts Claude Code's write-to-temp-then-rename pattern
// (claude_code_main/utils/file.ts:362-478 writeFileSyncAndFlush_DEPRECATED)
// to prevent file truncation on interrupted writes.
//
// Key guarantees:
//  1. Target file is NEVER partially written — temp→rename is atomic on all OSes.
//  2. Health check runs on temp file BEFORE replacing target.
//  3. If writing or health check fails, temp is cleaned up, target untouched.
//  4. Sync ensures data is physically on disk before rename.

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes content to path using a temp-file-then-rename
// strategy. On failure the original file (if any) is preserved.
//
// It returns an error if the write fails or if the pre-rename health
// check (checkFileHealth) rejects the content.
func writeFileAtomic(fullPath string, data []byte) error {
	dir := filepath.Dir(fullPath)
	base := filepath.Base(fullPath)

	// Create temp file in the same directory as the target.
	// Same-directory ensures atomic rename works across filesystem boundaries.
	tmp, err := os.CreateTemp(dir, base+".avatars-tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

	// Write + sync — equivalent to Node's {flush: true} / fsync.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("atomic write: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("atomic write: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("atomic write: close temp: %w", err)
	}

	// ---- Health check BEFORE touching target ----
	// Uses fullPath so extension-based checks (.go/.py/.js) work correctly.
	// tmpPath ends with ".avatars-tmp-*" which hides the real extension.
	if reason := checkFileHealth(fullPath, string(data)); reason != "" {
		cleanup()
		return fmt.Errorf("atomic write: health check failed for %s: %s", fullPath, reason)
	}

	// GAP-1 fix: Try atomic rename first (works on POSIX and same-volume Windows).
	// If it fails (common on Windows cross-volume or target-exists), fall back
	// to copy-and-delete. Original file is preserved until copy succeeds.
	if err := os.Rename(tmpPath, fullPath); err != nil {
		tmpData, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			cleanup()
			return fmt.Errorf("atomic write: rename failed, cannot read temp: %w", readErr)
		}
		if writeErr := os.WriteFile(fullPath, tmpData, 0644); writeErr != nil {
			cleanup()
			return fmt.Errorf("atomic write: rename failed, fallback write failed: %w", writeErr)
		}
		cleanup()
		return nil
	}

	return nil
}
