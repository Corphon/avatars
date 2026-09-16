package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"avatars/internal/tools"
)

type CodingRollbackArtifact struct {
	SessionID       string    `json:"session_id"`
	RunID           string    `json:"run_id"`
	TaskID          string    `json:"task_id"`
	AvatarID        string    `json:"avatar_id,omitempty"`
	ActionID        string    `json:"action_id,omitempty"`
	Tool            string    `json:"tool"`
	Operation       string    `json:"operation"`
	ProposalID      string    `json:"proposal_id,omitempty"`
	Intent          string    `json:"intent,omitempty"`
	ExpectedTargets []string  `json:"expected_targets,omitempty"`
	TargetPath      string    `json:"target_path,omitempty"`
	WorkingDir      string    `json:"working_dir,omitempty"`
	ExistedBefore   bool      `json:"existed_before"`
	BeforeSHA256    string    `json:"before_sha256,omitempty"`
	BeforeContent   string    `json:"before_content,omitempty"`
	AfterSHA256     string    `json:"after_sha256,omitempty"`
	CapturedAt      time.Time `json:"captured_at"`
}

func (e *Engine) persistCodingRollbackArtifact(envelope toolCallEnvelope, actionID string, toolName string, operation string, input any, payload map[string]any) (string, error) {
	if e == nil || e.memory == nil {
		return "", nil
	}
	targetPath, workingDir, ok := rollbackTargetForInput(toolName, input)
	if !ok {
		return "", nil
	}
	memoryPath := strings.TrimSpace(e.memory.Path())
	if memoryPath == "" {
		return "", nil
	}
	resolvedTarget, existedBefore, beforeContent, beforeHash, err := readRollbackBeforeImage(workingDir, targetPath)
	if err != nil {
		return "", err
	}
	artifact := CodingRollbackArtifact{
		SessionID:       e.sessionID,
		RunID:           strings.TrimSpace(envelope.runID),
		TaskID:          strings.TrimSpace(envelope.taskID),
		AvatarID:        strings.TrimSpace(envelope.avatarID),
		ActionID:        strings.TrimSpace(actionID),
		Tool:            strings.TrimSpace(toolName),
		Operation:       strings.TrimSpace(operation),
		ProposalID:      payloadTrimmedString(payload, "proposal_id"),
		Intent:          payloadTrimmedString(payload, "intent"),
		ExpectedTargets: payloadStringList(payload, "expected_targets"),
		TargetPath:      resolvedTarget,
		WorkingDir:      workingDir,
		ExistedBefore:   existedBefore,
		BeforeSHA256:    beforeHash,
		BeforeContent:   beforeContent,
		CapturedAt:      time.Now().UTC(),
	}
	artifactDir := filepath.Join(filepath.Dir(memoryPath), "rollback")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", fmt.Errorf("create rollback artifact directory: %w", err)
	}
	artifactPath := filepath.Join(artifactDir, fmt.Sprintf("%s.json", sanitizeArtifactName(rollbackArtifactName(artifact))))
	content, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal rollback artifact: %w", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write rollback artifact: %w", err)
	}
	return artifactPath, nil
}

func (e *Engine) finalizeCodingRollbackArtifact(artifactPath string) error {
	if e == nil || strings.TrimSpace(artifactPath) == "" {
		return nil
	}
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		return err
	}
	var artifact CodingRollbackArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		return err
	}
	targetPath := strings.TrimSpace(artifact.TargetPath)
	if targetPath == "" {
		return nil
	}
	targetContent, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if artifact.ExistedBefore {
				artifact.AfterSHA256 = ""
			} else {
				artifact.AfterSHA256 = ""
			}
		} else {
			return err
		}
	} else {
		sum := sha256.Sum256(targetContent)
		artifact.AfterSHA256 = hex.EncodeToString(sum[:])
	}
	updated, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(artifactPath, append(updated, '\n'), 0o644)
}

func rollbackTargetForInput(toolName string, input any) (string, string, bool) {
	switch strings.TrimSpace(toolName) {
	case "write":
		switch value := input.(type) {
		case tools.WriteInput:
			return value.Path, value.WorkingDir, true
		case *tools.WriteInput:
			if value != nil {
				return value.Path, value.WorkingDir, true
			}
		}
	case "patch":
		switch value := input.(type) {
		case tools.PatchInput:
			return value.Path, value.WorkingDir, true
		case *tools.PatchInput:
			if value != nil {
				return value.Path, value.WorkingDir, true
			}
		}
	}
	return "", "", false
}

func readRollbackBeforeImage(workingDir string, path string) (string, bool, string, string, error) {
	trimmedDir := strings.TrimSpace(workingDir)
	trimmedPath := strings.TrimSpace(path)
	if trimmedDir == "" || trimmedPath == "" {
		return "", false, "", "", fmt.Errorf("rollback artifact requires working dir and path")
	}
	resolvedDir, err := filepath.Abs(filepath.Clean(trimmedDir))
	if err != nil {
		return "", false, "", "", err
	}
	target := trimmedPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedDir, target)
	}
	resolvedTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", false, "", "", err
	}
	content, err := os.ReadFile(resolvedTarget)
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedTarget, false, "", "", nil
		}
		return "", false, "", "", err
	}
	sum := sha256.Sum256(content)
	return resolvedTarget, true, string(content), hex.EncodeToString(sum[:]), nil
}

func rollbackArtifactName(artifact CodingRollbackArtifact) string {
	seed := strings.Join([]string{artifact.RunID, artifact.ActionID, artifact.Tool, artifact.TargetPath, artifact.CapturedAt.Format(time.RFC3339Nano)}, "|")
	sum := sha256.Sum256([]byte(seed))
	shortHash := hex.EncodeToString(sum[:])[:16]
	prefix := strings.TrimSpace(artifact.RunID)
	if prefix == "" {
		prefix = "run"
	}
	name := sanitizeArtifactName(prefix)
	if len(name) > 48 {
		name = name[:48]
	}
	tool := sanitizeArtifactName(artifact.Tool)
	if tool == "" {
		tool = "tool"
	}
	joined := fmt.Sprintf("%s-%s-%s", name, tool, shortHash)
	if strings.TrimSpace(joined) == "" {
		return "rollback-artifact"
	}
	return joined
}
