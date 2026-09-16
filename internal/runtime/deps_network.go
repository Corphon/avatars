package runtime

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

// looksLikeDependencyNetworkFailure reports package-manager failures caused by
// registry/proxy/download issues (W1) — not application source defects.
// Cross-lang: go mod tidy, npm install, pip, cargo fetch.
func looksLikeDependencyNetworkFailure(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	if lower == "" {
		return false
	}
	tokens := []string{
		"downloading ",
		"proxy.golang",
		"goproxy",
		"dial tcp",
		"i/o timeout",
		"tls handshake",
		"connection reset",
		"connection refused",
		"no such host",
		"temporary failure in name resolution",
		"429 too many",
		"503 service",
		"502 bad gateway",
		"npm err! network",
		"npm error network",
		"etrimedout",
		"econnreset",
		"enotfound",
		"could not fetch",
		"failed to fetch",
		"failed to download",
		"read timed out",
		"cargo fetch",
		"failed to download replacement",
		"error downloading",
		"get \"https://",
		"go mod tidy failed",
		"go: downloading",
	}
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// runGoModTidyWithRetry runs go mod tidy up to attempts times (W1 network flake).
func runGoModTidyWithRetry(wd string, attempts int) error {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
		last = runGoModTidy(wd)
		if last == nil {
			return nil
		}
		if !looksLikeDependencyNetworkFailure(last.Error()) &&
			!strings.Contains(strings.ToLower(last.Error()), "go mod tidy") {
			return last
		}
	}
	return last
}

// withInheritedProxyEnv ensures GOPROXY/GOMODCACHE (and npm/pip peers) survive
// into child package-manager processes when the parent set them (W1).
func withInheritedProxyEnv(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	env := os.Environ()
	// Prefer existing env; only fill defaults when unset so local proxies still win.
	if os.Getenv("GOPROXY") == "" {
		env = append(env, "GOPROXY=https://proxy.golang.org,direct")
	}
	cmd.Env = env
}
