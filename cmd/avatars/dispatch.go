package main

import (
	"errors"

	"avatars/internal/llm"
	"avatars/internal/planner"
)

// C1.1: command dispatch. run -> runTask; unknown token -> runIntent.
func run(args []string) error {
	// Wire the commander LLM factory so the planner can ask the LLM
	// for a complexity assessment before producing a plan. The
	// factory matches the same shape used elsewhere in the package
	// (bootstrapIntentLLMClient) — a fresh client per call, with a
	// cleanup callback. The planner.Assess helper falls back to its
	// deterministic keyword+length heuristic when the LLM path
	// returns an error or unparseable output, so this wire-up is
	// safe even if no LLM is configured.
	planner.ComplexityLLMFactory = func() (llm.Client, func(), error) {
		return bootstrapIntentLLMClient()
	}
	if len(args) == 0 {
		return runREPL(nil)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return errors.New(usageText)
	}

	switch args[0] {
	case "run":
		return runTask(args[1:])
	case "intent":
		return runIntent(args[1:])
	case "repl":
		return runREPL(args[1:])
	case "bootstrap":
		return runBootstrap(args[1:])
	case "script":
		return runScript(args[1:])
	case "edit":
		return runEdit(args[1:])
	case "edit-many":
		return runEditMany(args[1:])
	case "resume":
		return runResume(args[1:])
	case "tasks":
		return runTasks(args[1:])
	case "memory":
		return runMemory(args[1:])
	case "rollback":
		return runRollback(args[1:])
	case "governance":
		return runGovernance(args[1:])
	case "feedback":
		return runFeedback(args[1:])
	case "stage":
		return runStage(args[1:])
	case "serve":
		return serveStage(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "smoke":
		return runSmoke(args[1:])
	case "actions":
		return runActions(args[1:])
	case "route":
		return runRoute(args[1:])
	case "config":
		return runConfig(args[1:])
	case "llm":
		return runLLM(args[1:])
	case "shell":
		return runShell(args[1:])
	case "write":
		return runWrite(args[1:])
	case "patch":
		return runPatch(args[1:])
	case "git":
		return runGit(args[1:])
	case "mcp":
		return runMCPCall(args[1:])
	case "plugins":
		return runPlugins(args[1:])
	case "arch":
		return runArch(args[1:])
	case "skills":
		return runSkills(args[1:])
	default:
		if len(args) > 0 {
			return runIntent(args)
		}
		return errors.New(usageText)
	}

}
