package tools

import "context"

type Definition interface {
	Name() string
	IsConcurrencySafe(input any) bool
	Call(ctx context.Context, input any) (Result, error)
}

type Result struct {
	Content string
}

type ReadInput struct {
	Path string
}

type ShellInput struct {
	Command         []string
	WorkingDir      string
	TimeoutMillis   int
	ProposalID      string
	Intent          string
	ExpectedTargets []string
}

type WriteInput struct {
	Path            string
	Content         string
	WorkingDir      string
	Overwrite       bool
	ProposalID      string
	Intent          string
	ExpectedTargets []string
}

type PatchInput struct {
	Path            string
	Old             string
	New             string
	WorkingDir      string
	ReplaceAll      bool
	ProposalID      string
	Intent          string
	ExpectedTargets []string
}

type GitInput struct {
	Args          []string
	WorkingDir    string
	TimeoutMillis int
}
