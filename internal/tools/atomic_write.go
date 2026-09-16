package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomicTool writes content to targetPath using a temp-file-then-rename
// strategy. On failure, the original file (if any) is preserved.
//
// This is the shared atomic write helper for patch.go and precise_edit.go,
// extracted to match write.go's P1-fix pattern (BUG-D / SEC-2).
func writeFileAtomicTool(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)

	tmp, err := os.CreateTemp(dir, base+".avatars-tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

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

	// Validate the temp content before touching the target.
	if errMsg := validateFileAfterEdit(tmpPath, targetPath); errMsg != "" {
		cleanup()
		return fmt.Errorf("%s", errMsg)
	}

	// Try rename first; fall back to copy if cross-volume.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		tmpData, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			cleanup()
			return fmt.Errorf("atomic write: rename failed, cannot read temp: %w", readErr)
		}
		if writeErr := os.WriteFile(targetPath, tmpData, perm); writeErr != nil {
			cleanup()
			return fmt.Errorf("atomic write: rename failed, fallback write failed: %w", writeErr)
		}
		cleanup()
	}
	return nil
}
