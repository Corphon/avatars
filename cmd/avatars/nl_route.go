package main

import (
	"errors"
	"fmt"
	"strings"

	"avatars/internal/runtime"
)

// C1.1: NL route dry-run (avatars route), separate from runTask.
func runRoute(args []string) error {
	options, err := parseRouteOptions(args)
	if err != nil {
		return err
	}
	input := strings.TrimSpace(options.Input)
	if input == "" {
		return errors.New("route input cannot be empty")
	}
	fmt.Printf("Route input: %s\n", input)
	route, handled, err := resolveNaturalLanguageQuestion(input, replOptionsFromRun(options), naturalLanguageContextREPL)
	if err != nil {
		return err
	}
	candidates := routeIntent(input)
	if handled && route.Kind != naturalLanguageRouteSafeRun {
		printNaturalLanguageRoute(route)
		fmt.Println("Route mode: dry-run; no command executed.")
		return nil
	}
	if handled && route.Kind == naturalLanguageRouteSafeRun && shouldExecuteNaturalLanguageSafeRun(route, candidates) {
		printNaturalLanguageRoute(route)
		fmt.Println("Route mode: dry-run; no command executed.")
		return nil
	}
	if len(candidates) > 0 {
		fmt.Println("Intent candidates:")
		for index, candidate := range candidates {
			fmt.Printf("%d. %s | confidence=%d | auto_safe=%s | needs_choice=%s | command=avatars %s\n", index+1, candidate.Summary, candidate.Confidence, yesNo(candidate.AutoSafe), yesNo(candidate.NeedsChoice), strings.Join(candidate.Command, " "))
		}
		fmt.Println("Route mode: dry-run; no command executed.")
		return nil
	}
	if handled {
		printNaturalLanguageRoute(route)
		fmt.Println("Route mode: dry-run; no command executed.")
		return nil
	}
	printNaturalLanguageRoutePreview(input, options)
	fmt.Println("Route mode: dry-run; no command executed.")
	return nil
}

func printNaturalLanguageRoute(route naturalLanguageRoute) {
	fmt.Printf("Natural language route: %s\n", route.Kind)
	if route.Summary != "" {
		fmt.Printf("Reason: %s\n", route.Summary)
	}
	if route.Confidence > 0 {
		fmt.Printf("Confidence: %d\n", route.Confidence)
	}
	if len(route.Command) > 0 {
		fmt.Printf("Command: avatars %s\n", strings.Join(route.Command, " "))
	}
	if strings.TrimSpace(route.Answer) != "" {
		fmt.Println("Answer preview:")
		fmt.Println(route.Answer)
	}
	if route.Kind == naturalLanguageRouteClarify {
		fmt.Printf("Clarify: %s\n", route.Question)
	}
}

func printNaturalLanguageRoutePreview(input string, options runCommandOptions) {
	decision := classifyNaturalLanguageQuestionWithoutLLM(input, replOptionsFromRun(options), naturalLanguageContextREPL)
	fmt.Printf("Natural language route: %s\n", smokeRouteLabel(decision.Kind))
	if decision.Reason != "" {
		fmt.Printf("Reason: %s\n", decision.Reason)
	}
	if decision.Confidence > 0 {
		fmt.Printf("Confidence: %d\n", decision.Confidence)
	}
	if len(decision.Command) > 0 {
		fmt.Printf("Command: avatars %s\n", strings.Join(decision.Command, " "))
	}
	if strings.TrimSpace(decision.Answer) != "" {
		fmt.Println("Answer preview:")
		fmt.Println(decision.Answer)
	}
	if decision.Kind == naturalLanguageDecisionClarify {
		fmt.Printf("Clarify: %s\n", naturalLanguageClarifyingQuestion(decision.Reason))
	}
}
func replOptionsFromRun(options runCommandOptions) replCommandOptions {
	repl := replCommandOptions{TaskID: options.TaskID, ForceNewTask: options.ForceNewTask}
	if options.PermissionMode != "" {
		repl.PermissionMode = string(options.PermissionMode)
	}
	return repl
}

func parseRouteOptions(args []string) (runCommandOptions, error) {
	options := runCommandOptions{}
	for len(args) > 0 {
		switch args[0] {
		case "--task":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars route [--task <task-id>] [--new-task] [--permission-mode <mode>] \"<intent>\"")
			}
			options.TaskID = strings.TrimSpace(args[1])
			args = args[2:]
		case "--new-task":
			options.ForceNewTask = true
			args = args[1:]
		case "--permission-mode":
			if len(args) < 2 {
				return runCommandOptions{}, errors.New("usage: avatars route [--task <task-id>] [--new-task] [--permission-mode <mode>] \"<intent>\"")
			}
			permissionMode, err := runtime.ParsePermissionMode(args[1])
			if err != nil {
				return runCommandOptions{}, err
			}
			options.PermissionMode = permissionMode
			args = args[2:]
		default:
			options.Input = strings.TrimSpace(strings.Join(args, " "))
			return options, nil
		}
	}
	return options, nil
}
