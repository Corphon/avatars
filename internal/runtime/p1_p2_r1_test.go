package runtime

import (
	"strings"
	"testing"
)

func TestClipCommandOutput_PrefersFailureTail(t *testing.T) {
	warn := strings.Repeat("PytestDeprecationWarning: asyncio_default_fixture_loop_scope is unset.\n", 40)
	fail := "FAILED tests/test_expenses.py::test_create - assert 500 == 201\nshort test summary info\n"
	out := warn + fail
	clipped := clipCommandOutput(out, 500)
	if !strings.Contains(clipped, "FAILED") && !strings.Contains(clipped, "short test summary") {
		preview := clipped
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Fatalf("expected failure body kept, got %q", preview)
	}
	if strings.HasPrefix(clipped, "PytestDeprecationWarning") && !strings.Contains(clipped, "FAILED") {
		t.Fatal("leading warning alone must not be the only retained content")
	}
}

func TestTomlDuplicateKeyReason_Filterwarnings(t *testing.T) {
	bad := `[tool.pytest.ini_options]
asyncio_default_fixture_loop_scope = "function"
filterwarnings = [
    "ignore::pytest.PytestDeprecationWarning:pytest_asyncio",
]
filterwarnings = [
    "ignore::pytest.PytestDeprecationWarning:pytest_asyncio",
]
`
	if reason := tomlDuplicateKeyReason("pyproject.toml", bad); reason == "" {
		t.Fatal("expected duplicate filterwarnings reject")
	}
	good := `[tool.pytest.ini_options]
asyncio_default_fixture_loop_scope = "function"
filterwarnings = [
    "ignore::pytest.PytestDeprecationWarning:pytest_asyncio",
]
`
	if reason := tomlDuplicateKeyReason("pyproject.toml", good); reason != "" {
		t.Fatalf("valid toml rejected: %s", reason)
	}
}

func TestCheckFileHealth_RejectsDuplicateToml(t *testing.T) {
	bad := "[tool.pytest.ini_options]\nfilterwarnings = []\nfilterwarnings = []\n"
	if reason := checkFileHealth("pyproject.toml", bad); reason == "" {
		t.Fatal("expected health reject for duplicate TOML key")
	}
}

func TestLooksLikeMissingPackageDeps_LocalApp(t *testing.T) {
	msg := `pytest failed: ImportError while loading conftest.\nE   ModuleNotFoundError: No module named 'app'`
	if looksLikeMissingPackageDeps(msg) {
		t.Fatal("local 'app' must NOT classify as missing package deps")
	}
	if !looksLikeMissingPackageDeps("pytest failed: ModuleNotFoundError: No module named 'fastapi'") {
		t.Fatal("third-party fastapi should still classify as missing deps")
	}
}
