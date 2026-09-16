package runtime

import "testing"

func TestLooksLikeDependencyNetworkFailure_GoTidy(t *testing.T) {
	msg := `go mod tidy: exit status 1: go: downloading modernc.org/sqlite v1.30.1
go: modernc.org/sqlite@v1.30.1: Get "https://proxy.golang.org/modernc.org/sqlite/@v/v1.30.1.zip": dial tcp: i/o timeout`
	if !looksLikeDependencyNetworkFailure(msg) {
		t.Fatal("expected network classification for go mod tidy download timeout")
	}
	if !looksLikeDependencyNetworkFailure("npm ERR! network ETIMEDOUT") {
		t.Fatal("expected npm network classification")
	}
	if looksLikeDependencyNetworkFailure("go build failed: undefined: Foo") {
		t.Fatal("app compile errors must not classify as deps network")
	}
}

func TestHealthFailureFingerprint_DepsNetwork(t *testing.T) {
	fp := healthFailureFingerprint(`go mod tidy failed: downloading foo: dial tcp i/o timeout`)
	if fp != "deps_network" {
		t.Fatalf("got %q want deps_network", fp)
	}
}

func TestHollowHTTPEntrypointReason_EmptyMain(t *testing.T) {
	body := "package main\n\nfunc main() {}\n"
	reason := hollowHTTPEntrypointReason("cmd/server/main.go", body, true)
	if reason == "" {
		t.Fatal("expected hollow empty main rejection")
	}
	wired := `package main
import "net/http"
func main() { http.ListenAndServe(":8080", nil) }
`
	if got := hollowHTTPEntrypointReason("cmd/server/main.go", wired, true); got != "" {
		t.Fatalf("wired main rejected: %s", got)
	}
}

func TestHollowNonGoHTTPEntrypointReason_EmptyServerJS(t *testing.T) {
	if reason := hollowNonGoHTTPEntrypointReason("src/server.js", "const x = 1;\n"); reason == "" {
		t.Fatal("expected empty server.js rejection")
	}
	wired := `const express = require('express');
const app = express();
app.listen(3000);
`
	if got := hollowNonGoHTTPEntrypointReason("src/server.js", wired); got != "" {
		t.Fatalf("wired server.js rejected: %s", got)
	}
}
