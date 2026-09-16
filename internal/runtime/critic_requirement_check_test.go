package runtime

import (
	"strings"
	"testing"
)

func TestF103_InjectableClockMustBePublicConfig(t *testing.T) {
	req := "tokenbucketx with injectable Clock on Config; Allow/Wait/Peek; tests must use fake time"
	// Interface exists but Config has no Clock — the r33 miss.
	code := `
package tokenbucketx
type Clock interface { Now() time.Time }
type Config struct { Capacity int; Rate float64 }
func New(cfg Config) *Bucket { return &Bucket{} }
func (b *Bucket) Allow() bool { return true }
func (b *Bucket) Wait(ctx context.Context) error { <-time.After(time.Second); return nil }
func (b *Bucket) Peek() int { return 0 }
`
	gaps := checkPlannedPublicAPIGaps(req, code)
	joined := strings.Join(gaps, "; ")
	if !strings.Contains(joined, "injectable clock") {
		t.Fatalf("expected public clock gap, got %q", joined)
	}
	if !strings.Contains(joined, "Wait/Sleep/After") {
		t.Fatalf("expected Wait wall-time gap, got %q", joined)
	}

	fixed := `
package tokenbucketx
type Clock interface { Now() time.Time }
type Config struct { Capacity int; Rate float64; Clock Clock }
func New(cfg Config) *Bucket { return &Bucket{clock: cfg.Clock} }
func (b *Bucket) Wait(ctx context.Context) error { _ = b.clock.Now(); return nil }
`
	if gaps := checkPlannedPublicAPIGaps(req, fixed); len(gaps) > 0 {
		t.Fatalf("fixed Go Config.Clock should pass, got %v", gaps)
	}
}

func TestF103_InjectableClockPythonAndJS(t *testing.T) {
	req := "rate limiter with injectable clock for tests"
	py := "class Config:\n    def __init__(self, clock=None):\n        self.clock = clock\n"
	if gaps := checkPlannedPublicAPIGaps(req, py); len(gaps) > 0 {
		t.Fatalf("python clock= should pass, got %v", gaps)
	}
	js2 := "class Limiter { constructor({ clock } = {}) { this.clock = clock } wait() { this.clock.now() } }\n"
	if gaps := checkPlannedPublicAPIGaps(req, js2); len(gaps) > 0 {
		t.Fatalf("js constructor clock should pass, got %v", gaps)
	}
}

func TestWaitPollingClockStillFlagsWallSleep(t *testing.T) {
	req := "in-process retry library with injectable clock; tests must advance time without wall sleep"
	hang := `
package retryx
type Clock interface { Now() time.Time }
type Config struct { Clock Clock }
func wait(cfg Config, delay time.Duration) {
	start := cfg.Clock.Now()
	target := start.Add(delay)
	for cfg.Clock.Now().Before(target) {
		time.Sleep(time.Millisecond)
	}
}
`
	gaps := checkPlannedPublicAPIGaps(req, hang)
	joined := strings.Join(gaps, "; ")
	if !strings.Contains(joined, "Wait/Sleep/After") {
		t.Fatalf("polling Clock.Now with time.Sleep must still flag wall wait, got %q", joined)
	}
	pyHang := "class Config:\n    def __init__(self, clock=None):\n        self.clock = clock\n" +
		"def wait(cfg, delay):\n    import time\n    while cfg.clock.now() < end:\n        time.sleep(0.001)\n"
	if gaps := checkPlannedPublicAPIGaps(req, pyHang); !strings.Contains(strings.Join(gaps, "; "), "Wait/Sleep/After") {
		t.Fatalf("python wait+time.sleep must flag, got %v", gaps)
	}
}

func TestF103_BacktickedPublicAPICrossLang(t *testing.T) {
	req := "library must expose `allow` and `peek`"
	py := "def allow():\n    return True\ndef peek():\n    return 0\n"
	if gaps := checkPlannedPublicAPIGaps(req, py); len(gaps) > 0 {
		t.Fatalf("python defs should satisfy backticked API, got %v", gaps)
	}
	missing := checkPlannedPublicAPIGaps(req, "def other():\n    return 1\n")
	joined := strings.Join(missing, "; ")
	if !strings.Contains(joined, "allow") && !strings.Contains(joined, "peek") {
		t.Fatalf("expected missing allow/peek, got %q", joined)
	}
}
