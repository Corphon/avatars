package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"avatars/internal/writejail"
)

// generatedSkillWritePath picks where the write tool should persist a generated
// skill. Skills live under AVATARS_HOME/skills (any language / any project).
// Rel-ing a HOME path onto a sibling project cwd can collapse to a basename
// and dump the markdown into the project root (F112).
func generatedSkillWritePath(workingDir, preparedPath string) string {
	preparedPath = strings.TrimSpace(preparedPath)
	if preparedPath == "" {
		return preparedPath
	}
	absPrepared := preparedPath
	if p, err := filepath.Abs(preparedPath); err == nil {
		absPrepared = p
	}
	if writejail.IsAllowedExtraRoot(absPrepared) {
		return absPrepared
	}
	base := filepath.Base(absPrepared)
	if home := strings.TrimSpace(os.Getenv("AVATARS_HOME")); home != "" {
		homeSkills := filepath.Join(home, "skills", "generated", base)
		if absHome, err := filepath.Abs(homeSkills); err == nil && writejail.IsAllowedExtraRoot(absHome) {
			// Project-root basename or other out-of-layout path → HOME.
			if filepath.Base(filepath.Dir(absPrepared)) != "generated" {
				return absHome
			}
		}
	}
	if strings.TrimSpace(workingDir) == "" {
		return absPrepared
	}
	rel, err := filepath.Rel(workingDir, absPrepared)
	if err != nil {
		return absPrepared
	}
	relSlash := filepath.ToSlash(rel)
	if relSlash == "" || relSlash == "." || strings.HasPrefix(relSlash, "../") {
		return absPrepared
	}
	if filepath.Dir(relSlash) == "." {
		return absPrepared
	}
	return relSlash
}

func writeGeneratedSkillDirect(preparedPath, content string) error {
	preparedPath = strings.TrimSpace(preparedPath)
	if preparedPath == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(preparedPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(preparedPath, []byte(content), 0o644)
}

// recoverGeneratedSkillIfMissing moves a leaked project-root copy into the
// canonical HOME path when FinalizeGenerated would otherwise read a missing file.
func recoverGeneratedSkillIfMissing(preparedPath, writePath, workingDir string) error {
	preparedPath = strings.TrimSpace(preparedPath)
	if preparedPath == "" {
		return os.ErrInvalid
	}
	if _, err := os.Stat(preparedPath); err == nil {
		return nil
	}
	base := filepath.Base(preparedPath)
	candidates := []string{
		writePath,
		filepath.Join(workingDir, writePath),
		filepath.Join(workingDir, base),
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !filepath.IsAbs(c) {
			c = filepath.Join(workingDir, c)
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if seen[abs] || abs == preparedPath {
			continue
		}
		seen[abs] = true
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(preparedPath), 0o755); err != nil {
			return err
		}
		if err := os.Rename(abs, preparedPath); err == nil {
			return nil
		}
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			continue
		}
		if err := os.WriteFile(preparedPath, data, 0o644); err == nil {
			_ = os.Remove(abs)
			return nil
		}
	}
	return os.ErrNotExist
}

func isSandboxEscapeWriteErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "escapes sandbox") || strings.Contains(msg, "write tool target escapes")
}
