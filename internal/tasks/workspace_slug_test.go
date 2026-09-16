package tasks

import (
	"strings"
	"testing"
)

func TestDeriveTaskID_StripsNegationClauses(t *testing.T) {
	title := "用自然语言从零做一个全新的 Go CLI 项目，领域固定叫 csvmesh（多 CSV 拼接）。\n" +
		"不要做成 HTTP API、webhook、投票问卷、物流追踪、REST+SQLite 服务——那些旧题不要再用。\n" +
		"技术约束：Go 1.22+；go mod init example.com/csvmesh"
	id := deriveTaskID(title)
	lower := strings.ToLower(id)
	for _, bad := range []string{"http", "webhook", "sqlite", "rest", "投票", "物流"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("slug still contains negation residue %q: %s", bad, id)
		}
	}
	if !strings.Contains(lower, "csvmesh") && !strings.Contains(lower, "cli") && !strings.Contains(lower, "csv") {
		// Hash suffix always present; positive domain should survive when ASCII.
		t.Logf("id=%s (chinese domain may be stripped by sanitize; ensure no negatives)", id)
	}
	// English variant
	en := deriveTaskID("Build csvmesh Go CLI. Do not make an HTTP API webhook or REST SQLite service.")
	enLower := strings.ToLower(en)
	for _, bad := range []string{"http", "webhook", "sqlite", "rest"} {
		if strings.Contains(enLower, bad) {
			t.Fatalf("en slug contains %q: %s", bad, en)
		}
	}
	if !strings.Contains(enLower, "csvmesh") {
		t.Fatalf("expected csvmesh in slug: %s", en)
	}
	// F5/r11: parenthetical "不是 CSV" must not inject csv into a non-CSV domain slug.
	cidr := deriveTaskID("做 Go 项目 cidrkit（IPv4 CIDR 库；不是网站、不是 CSV、不是数据库）。标准库 net。")
	cidrLower := strings.ToLower(cidr)
	if strings.Contains(cidrLower, "csv") || strings.Contains(cidrLower, "sqlite") || strings.Contains(cidrLower, "http") {
		t.Fatalf("cidrkit slug polluted: %s", cidr)
	}
	// F5′/r13: "不要再做 CSV / Markdown HTTP / CIDR / token-bucket / SQLite" list form.
	cron := deriveTaskID("用自然语言从零做一个全新的 Go 小项目，主题和以往都不同（不要再做 CSV / Markdown HTTP / CIDR / token-bucket / SQLite CRUD）。\n项目名建议：cronnext\n目标：5 字段 cron Next 库。")
	cronLower := strings.ToLower(cron)
	for _, bad := range []string{"csv", "markdown", "http", "cidr", "token", "bucket", "sqlite", "crud"} {
		if strings.Contains(cronLower, bad) {
			t.Fatalf("cronnext slug polluted with %q: %s", bad, cron)
		}
	}
	if !strings.Contains(cronLower, "cronnext") && !strings.Contains(cronLower, "cron") {
		t.Logf("cronnext id=%s (domain may be chinese-stripped; ensure no negatives)", cron)
	}

	ttl := deriveTaskID("从零做进程内 TTL 缓存库。Get/Set/Delete，可注入时钟。不要 HTTP 服务，也不要 CLI。go test 要过。")
	ttlLower := strings.ToLower(ttl)
	for _, bad := range []string{"cli", "http"} {
		if strings.Contains(ttlLower, bad) {
			t.Fatalf("ttlcache slug polluted with %q: %s", bad, ttl)
		}
	}
	enNoCLI := deriveTaskID("Build an in-process TTL cache library. Do not add a CLI or HTTP server. Unit tests required.")
	enNoCLILower := strings.ToLower(enNoCLI)
	for _, bad := range []string{"cli", "http"} {
		if strings.Contains(enNoCLILower, bad) {
			t.Fatalf("en ttl slug polluted with %q: %s", bad, enNoCLI)
		}
	}
	keepCLI := deriveTaskID("Build csvmesh Go CLI for merging CSV files.")
	if !strings.Contains(strings.ToLower(keepCLI), "cli") {
		t.Fatalf("positive CLI product should keep cli in slug: %s", keepCLI)
	}
	noSleep := deriveTaskID("Build retryx in-process retry library. Injectable clock. Do not use wall-clock sleep. Tests must pass.")
	if strings.Contains(strings.ToLower(noSleep), "sleep") {
		t.Fatalf("wall-clock sleep negation leaked into slug: %s", noSleep)
	}
	zhSleep := deriveTaskID("从零做 retryx。时钟可注入，不要用墙上 sleep 硬等。go test 要过。")
	if strings.Contains(strings.ToLower(zhSleep), "sleep") {
		t.Fatalf("不要用墙上 sleep leaked into slug: %s", zhSleep)
	}

	noBoot := deriveTaskID("搞错了吧，我不要命令行脚手架，也不要再 bootstrap。就是当前目录里一个能 import 的优先队列库，Push/Pop，go test 要绿。")
	noBootLower := strings.ToLower(noBoot)
	if strings.Contains(noBootLower, "bootstrap") {
		t.Fatalf("negated bootstrap leaked into slug: %s", noBoot)
	}

	flagTitle := "----permission-mode acceptEdits import go lru lrux in-process cache"
	flagID := deriveTaskID(flagTitle)
	flagLower := strings.ToLower(flagID)
	for _, bad := range []string{"permission", "acceptedits", "accept-edits"} {
		if strings.Contains(flagLower, bad) {
			t.Fatalf("leaked CLI flag residue %q in slug: %s", bad, flagID)
		}
	}
	if !strings.Contains(flagLower, "lrux") && !strings.Contains(flagLower, "lru") {
		t.Fatalf("expected lru/lrux in slug after stripping flags: %s", flagID)
	}
}
