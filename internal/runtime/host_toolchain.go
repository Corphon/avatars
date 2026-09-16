package runtime

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// hostToolchainUnavailablePrefix is prepended to health errors that Builder
// cannot fix by rewriting application code (R11-1).
const hostToolchainUnavailablePrefix = "host toolchain unavailable:"

// isHostToolchainFailure reports compile/test failures caused by missing or
// broken host toolchains (linker, C compiler, language runtime on PATH) —
// not by application source defects. Critic must not spin Builder rebuilds
// on these (R11-1 / R11-3).
func isHostToolchainFailure(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	if strings.HasPrefix(lower, strings.ToLower(hostToolchainUnavailablePrefix)) {
		return true
	}
	for _, token := range hostToolchainFailureTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// hostToolchainFailureTokens are substrings that indicate an environment /
// toolchain prerequisite failure across Rust / Go(CGO) / Node / JVM hosts.
var hostToolchainFailureTokens = []string{
	// Rust / cc-rs / mingw / msvc
	"failed to find tool \"gcc",
	"failed to find tool \"cc",
	"failed to find tool \"cl",
	"dlltool.exe",
	"error occurred in cc-rs",
	"linker `link.exe` not found",
	"linker 'link.exe' not found",
	"link.exe` not found",
	"link.exe' not found",
	"note: the msvc targets depend on the msvc linker",
	"please ensure that vs language tools",
	"visual studio",
	"vs code is a different product", // common mis-hint when Build Tools missing
	"is not a valid win32 application", // corrupt/wrong linker binary
	"x86_64-w64-mingw32",
	"cargo not found on path",
	"no such file or directory\n  gcc",
	"program not found (see https://docs.rs/cc",
	// Go CGO / linker
	"cgo: c compiler",
	"gcc: executable file not found",
	"clang: executable file not found",
	"ld: cannot find",
	// Node native addons / node-gyp / MSVC Build Tools (J2-2)
	"node-gyp",
	"gyp err!",
	"gyp: no xcode or clt",
	"could not find any visual studio",
	"could not find any vs",
	"you need to install the latest version of visual studio",
	"npm error gyp",
	"npm err! gyp",
	"node-gyp rebuild",
	"binding.gyp",
	"find vs",
	"msbuild.exe",
	"vcbuild.exe",
	"python is not set from command line or npm configuration",
	// Generic missing runtimes when manifests force checks
	"java: command not found",
	"javac: command not found",
	"'javac' is not recognized",
	"'java' is not recognized",
}

// isImportLayoutFailure reports ImportError / module-not-found style failures
// caused by dual module+package layouts or missing symbols (C4/C9).
func isImportLayoutFailure(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	for _, t := range []string{
		"importerror",
		"cannot import name",
		"modulenotfounderror",
		"no module named",
		"cannot find module",
		"err_module_not_found",
		"package not found",
		"no required module provides package",
		// F63: Go dual-package in one directory (lib + package main test).
		"found packages",
		"found package",
	} {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// annotateHostToolchainFailure prefixes known toolchain errors so downstream
// gates can short-circuit without re-parsing noisy compiler output.
func annotateHostToolchainFailure(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" || isHostToolchainFailure(msg) {
		if msg != "" && !strings.HasPrefix(strings.ToLower(msg), strings.ToLower(hostToolchainUnavailablePrefix)) {
			return hostToolchainUnavailablePrefix + " " + msg
		}
		return msg
	}
	return msg
}

// healthFailureFingerprint collapses a health/compile error into a stable
// key for same-cause circuit breaking (R11-3).
func healthFailureFingerprint(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	if isHostToolchainFailure(msg) {
		return "host_toolchain"
	}
	if isImportLayoutFailure(msg) {
		return "import_layout"
	}
	lower := strings.ToLower(msg)
	// T1: tsc/poison stub Invalid character — stable across path noise.
	if strings.Contains(lower, "invalid character") ||
		strings.Contains(lower, "poison stub") ||
		strings.Contains(lower, "comment-only stub") ||
		strings.Contains(lower, "junk/dependency scaffold") ||
		strings.Contains(lower, "junk scaffold") {
		return "poison_stub_compile"
	}
	// W1: registry/proxy/download flakes — more specific than missing-deps.
	if looksLikeDependencyNetworkFailure(msg) {
		return "deps_network"
	}
	// U1: missing package deps / types — stable across path noise.
	if looksLikeMissingPackageDeps(msg) {
		return "missing_package_deps"
	}
	// P2: TOML/config parse failures (duplicate keys etc.).
	if strings.Contains(lower, "cannot overwrite a value") ||
		strings.Contains(lower, "duplicate toml key") {
		return "config_parse"
	}
	// Keep a short normalized stem (drop volatile paths / line noise).
	stem := lower
	for _, cut := range []string{"\n", "\r", "\\", "/"} {
		if i := strings.Index(stem, cut); i > 0 && i < 160 {
			stem = stem[:i]
		}
	}
	if len(stem) > 180 {
		stem = stem[:180]
	}
	sum := sha256.Sum256([]byte(stem))
	return fmt.Sprintf("%x", sum[:8])
}
