package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsAttachmentSummarizePlaceholder(t *testing.T) {
	if !IsAttachmentSummarizePlaceholder("summarize this attached file") {
		t.Fatal("english placeholder")
	}
	if !IsAttachmentSummarizePlaceholder("  看看这个文件  ") {
		t.Fatal("chinese placeholder")
	}
	if IsAttachmentSummarizePlaceholder("Use avatars stage to write a webpage") {
		t.Fatal("real task must not look like a placeholder")
	}
}

func TestBootstrapWorkflowFromTask_SkipsPlaceholder(t *testing.T) {
	root := t.TempDir()
	if err := BootstrapWorkflowFromTask(root, "summarize this attached file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "workflow", "avatars_plan.md")); err == nil {
		t.Fatal("placeholder must not write workflow docs")
	}
}
