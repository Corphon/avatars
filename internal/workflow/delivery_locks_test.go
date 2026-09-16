package workflow

import (
	"strings"
	"testing"
)

func TestDeliveryLocksUserNote_MinHeapAndName(t *testing.T) {
	req := "请在当前空目录做一个可以 import 的 Go 进程内优先队列库，名字叫 priorityqx。数字越小越优先。同优先级的按进入顺序出。"
	got := DeliveryLocksUserNote(req)
	if got == "" {
		t.Fatal("expected locks")
	}
	for _, want := range []string{"priorityqx", "min-heap", "FIFO"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	sys, user := BuildPlanPrompt(req, "demo")
	if strings.Contains(sys, "DELIVERY LOCKS") {
		t.Fatal("locks must stay off the planner system prefix")
	}
	if !strings.Contains(user, "priorityqx") {
		t.Fatalf("locks should be on the user turn, got tail %q", user[max(0, len(user)-240):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
