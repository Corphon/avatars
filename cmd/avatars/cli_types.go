package main

import (
	"avatars/internal/runtime"
)

// C1.1: shared CLI option types (NL + run).
type fileContextState struct {
	Path    string
	Content string
}

type runCommandOptions struct {
	TaskID            string
	ForceNewTask      bool
	ResumeTranscript  string
	ContinueFromPause bool
	PermissionMode    runtime.PermissionMode
	Input             string
	InputFile         string
	Progress          string // "", "0"/"off", "1"/"text", "json"
}

type bootstrapCommandOptions struct {
	Apply  bool
	Name   string
	Module string
	Stack  string
	Spec   string
}

type bootstrapContextArtifact struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	Stack  string `json:"stack"`
}

type scriptCommandOptions struct {
	Apply               bool
	ForceLLM            bool
	Path                string
	Description         string
	ExpectedOutput      string
	ExpectedOutputIsSet bool
}

type scriptScaffoldResult struct {
	Content        string
	Source         string
	ExpectedOutput string
}

type editCommandOptions struct {
	Apply       bool
	Path        string
	Instruction string
}

type editFileResult struct {
	Content string
	Source  string
}

type editManyCommandOptions struct {
	Apply       bool
	Instruction string
}

type editManyFileResult struct {
	Path    string
	Content string
}

type editManySourceFile struct {
	Path    string
	Content string
}

type serveCommandOptions struct {
	TaskID           string
	ResumeTranscript string
	PermissionMode   runtime.PermissionMode
	Input            string
	RunDemo          bool
}

type mcpCommandOptions struct {
	TaskID      string
	Positionals []string
}

type retryNodeDryRunOptions struct {
	TaskID           string
	OriginRunID      string
	PausePointID     string
	NodeID           string
	PausePointDigest string
	DryRun           bool
}
