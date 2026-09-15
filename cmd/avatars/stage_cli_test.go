package main

import (
	"strings"
	"testing"
)

func TestParseStageCLIArgs_EditTakesTrailingDirection(t *testing.T) {
	parsed, err := parseStageCLIArgs([]string{"--edit", "latest", "做成像素风"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.editID != "latest" || parsed.prompt != "做成像素风" {
		t.Fatalf("got editID=%q prompt=%q", parsed.editID, parsed.prompt)
	}
}

func TestParseStageCLIArgs_ServeOnlyHasNoInput(t *testing.T) {
	parsed, err := parseStageCLIArgs([]string{"--serve"})
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.startServe || parsed.projectMode || strings.Join(parsed.positional, " ") != "" {
		t.Fatalf("%+v", parsed)
	}
}

func TestParseStageCLIArgs_ProjectAndPrompt(t *testing.T) {
	parsed, err := parseStageCLIArgs([]string{"--project", "--prompt", "only show layers"})
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.projectMode || parsed.prompt != "only show layers" {
		t.Fatalf("%+v", parsed)
	}
}
