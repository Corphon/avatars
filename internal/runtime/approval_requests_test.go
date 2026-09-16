package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"avatars/internal/tools"
)

func TestApprovalRequestHMACRoundTrip(t *testing.T) {
	sessionID := "test-session-abc123"
	approvalKey := "write-/tmp/foo.txt"
	requestedAt := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	// Build a request identical to how persistPendingApprovalRequest does it.
	request, ok := buildPendingApprovalRequest(
		"write", "create",
		tools.WriteInput{Path: "/tmp/foo.txt", Content: "hello", WorkingDir: "/tmp", Overwrite: false},
		PermissionModeDefault,
		approvalKey,
		requestedAt,
	)
	if !ok {
		t.Fatal("buildPendingApprovalRequest returned false")
	}

	// Simulate what persistPendingApprovalRequest does before writing.
	request.SessionID = sessionID
	if err := request.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC failed: %v", err)
	}
	if strings.TrimSpace(request.HMAC) == "" {
		t.Fatal("expected HMAC to be non-empty after ComputeHMAC")
	}

	// Marshal → disk → unmarshal (simulating persist + load round-trip).
	content, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var loaded PendingApprovalRequest
	if err := json.Unmarshal(content, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// VerifyHMAC should succeed on the unmodified payload.
	if err := loaded.VerifyHMAC(); err != nil {
		t.Fatalf("VerifyHMAC failed on valid round-trip: %v", err)
	}

	// Corrupt the tool name and verify HMAC fails.
	loaded.Tool = "shell"
	if err := loaded.VerifyHMAC(); err == nil {
		t.Fatal("expected VerifyHMAC to fail after tampering with Tool field")
	} else {
		t.Logf("tamper detected correctly: %v", err)
	}
}

func TestApprovalRequestHMACTamperDetection(t *testing.T) {
	sessionID := "test-session-def456"
	approvalKey := "shell-rm-rf"

	request, ok := buildPendingApprovalRequest(
		"shell", "run",
		tools.ShellInput{Command: []string{"ls", "-la"}, WorkingDir: "/tmp"},
		PermissionModeDefault,
		approvalKey,
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("buildPendingApprovalRequest returned false")
	}

	request.SessionID = sessionID
	if err := request.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC failed: %v", err)
	}

	content, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Tamper with the file content: replace shell command.
	tampered := strings.Replace(string(content), `"ls"`, `"rm"`, 1)
	if tampered == string(content) {
		t.Fatal("tamper replacement had no effect")
	}

	var loaded PendingApprovalRequest
	if err := json.Unmarshal([]byte(tampered), &loaded); err != nil {
		t.Fatalf("unmarshal tampered content failed: %v", err)
	}

	if err := loaded.VerifyHMAC(); err == nil {
		t.Fatal("expected VerifyHMAC to fail on tampered shell command")
	}
}

func TestApprovalRequestHMACMissingSessionID(t *testing.T) {
	request := PendingApprovalRequest{
		ApprovalKey: "test-key",
		Tool:        "read",
		Operation:   "open",
		// SessionID intentionally left empty.
	}

	if err := request.ComputeHMAC(); err == nil {
		t.Fatal("expected ComputeHMAC to fail when SessionID is empty")
	}

	request.HMAC = "abc123"
	if err := request.VerifyHMAC(); err == nil {
		t.Fatal("expected VerifyHMAC to fail when SessionID is empty")
	}
}

func TestApprovalRequestHMACMissingTag(t *testing.T) {
	request := PendingApprovalRequest{
		ApprovalKey: "test-key",
		SessionID:   "test-session",
		Tool:        "read",
		Operation:   "open",
		// HMAC intentionally left empty.
	}

	if err := request.VerifyHMAC(); err == nil {
		t.Fatal("expected VerifyHMAC to fail when HMAC is empty")
	}
}

func TestApprovalRequestPersistAndLoadWithHMAC(t *testing.T) {
	// Integration test: write to real disk, load back, verify HMAC.
	tempDir := t.TempDir()
	memoryDir := filepath.Join(tempDir, "memory")
	approvalKey := "write-/tmp/bar.txt"

	request, ok := buildPendingApprovalRequest(
		"write", "create",
		tools.WriteInput{Path: "/tmp/bar.txt", Content: "world", WorkingDir: "/tmp", Overwrite: true},
		PermissionModeDefault,
		approvalKey,
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("buildPendingApprovalRequest returned false")
	}

	request.SessionID = "integration-test-session"
	if err := request.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC failed: %v", err)
	}

	// Write to disk (same flow as persistPendingApprovalRequest).
	artifactPath := PendingApprovalRequestPath(memoryDir, approvalKey)
	if artifactPath == "" {
		t.Fatal("PendingApprovalRequestPath returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	content, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := os.WriteFile(artifactPath, append(content, '\n'), 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Load back and verify HMAC passes.
	loaded, err := LoadPendingApprovalRequest(memoryDir, approvalKey)
	if err != nil {
		t.Fatalf("LoadPendingApprovalRequest failed: %v", err)
	}
	if loaded.Tool != "write" {
		t.Fatalf("loaded tool mismatch: %s", loaded.Tool)
	}

	// Tamper with the on-disk file: keep the HMAC field but change the tool.
	// This must be detected by HMAC verification on load.
	tampered := strings.Replace(string(content), `"tool": "write"`, `"tool": "shell"`, 1)
	if tampered == string(content) {
		t.Fatal("tamper replacement had no effect")
	}
	if err := os.WriteFile(artifactPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("write tampered file failed: %v", err)
	}
	if _, err := LoadPendingApprovalRequest(memoryDir, approvalKey); err == nil {
		t.Fatal("expected LoadPendingApprovalRequest to fail on tampered file")
	} else {
		t.Logf("tampered file rejected: %v", err)
	}
}

func TestApprovalRequestHMACCrossSessionMismatch(t *testing.T) {
	// HMAC is keyed with sessionID. A different session must produce a different HMAC.
	approvalKey := "write-/tmp/cross.txt"

	requestA, ok := buildPendingApprovalRequest(
		"write", "create",
		tools.WriteInput{Path: "/tmp/cross.txt", Content: "data", WorkingDir: "/tmp", Overwrite: false},
		PermissionModeDefault,
		approvalKey,
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("buildPendingApprovalRequest returned false")
	}
	requestA.SessionID = "session-alpha"
	if err := requestA.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC session-alpha failed: %v", err)
	}

	// Same payload, different session.
	requestB, _ := buildPendingApprovalRequest(
		"write", "create",
		tools.WriteInput{Path: "/tmp/cross.txt", Content: "data", WorkingDir: "/tmp", Overwrite: false},
		PermissionModeDefault,
		approvalKey,
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	requestB.SessionID = "session-beta"
	if err := requestB.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC session-beta failed: %v", err)
	}

	if requestA.HMAC == requestB.HMAC {
		t.Fatal("expected different HMAC values for different session IDs")
	}

	// VerifyHMAC with session-beta should pass on requestB, but not on requestA.
	if err := requestA.VerifyHMAC(); err != nil {
		t.Fatal("requestA should verify with its own sessionID")
	}
	if err := requestB.VerifyHMAC(); err != nil {
		t.Fatal("requestB should verify with its own sessionID")
	}
}

func TestApprovalRequestHMACDeterministic(t *testing.T) {
	// Same payload + same sessionID must produce the same HMAC.
	request1, ok := buildPendingApprovalRequest(
		"patch", "edit",
		tools.PatchInput{Path: "/tmp/f.go", Old: "a", New: "b", WorkingDir: "/tmp"},
		PermissionModeDefault,
		"patch-key",
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	if !ok {
		t.Fatal("buildPendingApprovalRequest failed")
	}
	request1.SessionID = "det-session"
	if err := request1.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC #1 failed: %v", err)
	}

	request2, _ := buildPendingApprovalRequest(
		"patch", "edit",
		tools.PatchInput{Path: "/tmp/f.go", Old: "a", New: "b", WorkingDir: "/tmp"},
		PermissionModeDefault,
		"patch-key",
		time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	)
	request2.SessionID = "det-session"
	if err := request2.ComputeHMAC(); err != nil {
		t.Fatalf("ComputeHMAC #2 failed: %v", err)
	}

	if request1.HMAC != request2.HMAC {
		t.Fatal("HMAC must be deterministic for identical payload and sessionID")
	}
}
