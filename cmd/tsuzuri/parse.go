package main

import (
	tsuzuri "github.com/utsuri-foundry/tsuzuri"
	"os"
	"slices"
	"strings"
)

type invocation struct {
	query            tsuzuri.Query
	coord            tsuzuri.Coordination
	write            bool
	author           bool
	requestID, input string
}

var coordinateCommands = []string{"claim", "accept", "handoff", "materialize", "release"}
var queryCommands = []string{"compatibility", "validate", "projects", "project", "list", "next", "show", "status", "doctor", "overview"}

func resolveOverviewColor(mode string, terminal, noColor bool, term string) string {
	if mode != "" && mode != "auto" {
		return mode
	}
	if terminal && !noColor && term != "dumb" {
		return "always"
	}
	return "never"
}

func parseInvocation(args []string) (invocation, error) {
	var v invocation
	positional := []string{}
	options := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			key, value, has := strings.Cut(a, "=")
			if _, ok := options[key]; ok {
				return v, usage("duplicate option " + key)
			}
			if slices.Contains([]string{"--no-worktree", "--refresh"}, key) {
				if has {
					return v, usage("unexpected option value")
				}
				options[key] = "true"
				continue
			}
			if !slices.Contains([]string{"--source", "--project", "--kind", "--canonical-state", "--claim-status", "--format", "--color", "--owner", "--worker", "--worktree", "--expected-digest", "--to", "--checkpoint", "--request-id", "--input"}, key) {
				return v, usage("unknown option " + key)
			}
			if !has {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
					return v, usage("missing value for " + key)
				}
				i++
				value = args[i]
			}
			options[key] = value
		} else if strings.HasPrefix(a, "-") {
			return v, usage("unknown option " + a)
		} else {
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		return v, usage("missing command")
	}
	command := positional[0]
	v.write = slices.Contains(coordinateCommands, command)
	v.author = slices.Contains([]string{"create-project", "create-task", "transition-task"}, command)
	if !v.write && !v.author && !slices.Contains(queryCommands, command) {
		return v, usage("unknown command " + command)
	}
	needsID := v.write || command == "show" || command == "project" || command == "transition-task"
	if needsID && len(positional) != 2 || !needsID && command != "status" && len(positional) != 1 || command == "status" && len(positional) > 2 {
		return v, usage("unexpected or missing identifier")
	}
	id := ""
	if len(positional) == 2 {
		id = positional[1]
	}
	allowed := map[string][]string{
		"create-project": {"--request-id", "--input"}, "create-task": {"--request-id", "--input"}, "transition-task": {"--request-id", "--input"}, "overview": {"--project", "--format", "--color"},
		"claim": {"--owner", "--worker", "--worktree", "--no-worktree", "--expected-digest"}, "accept": {"--worker", "--worktree", "--no-worktree"}, "handoff": {"--to", "--checkpoint"}, "materialize": {"--worktree"},
		"show": {"--source", "--refresh"}, "project": {"--source", "--refresh"}, "projects": {"--source", "--refresh"}, "validate": {"--source"}, "list": {"--project", "--kind", "--canonical-state", "--claim-status", "--refresh"}, "next": {"--project", "--kind"},
	}
	for key := range options {
		if !slices.Contains(allowed[command], key) {
			return v, usage("option " + key + " is not valid for " + command)
		}
	}
	if _, a := options["--worktree"]; a && options["--no-worktree"] != "" {
		return v, usage("--worktree conflicts with --no-worktree")
	}
	worker := options["--worker"]
	if command == "overview" && slices.Contains(args, "--json") {
		for _, key := range []string{"--format", "--color"} {
			if _, ok := options[key]; ok {
				return v, usage(key + " conflicts with --json")
			}
		}
	}
	v.requestID, v.input = options["--request-id"], options["--input"]
	if v.author && v.requestID == "" {
		return v, usage("authoring requires --request-id")
	}
	if _, set := options["--worker"]; !set && (command == "claim" || command == "accept") {
		worker = os.Getenv("TSUZURI_WORKER")
	}
	if command == "accept" && worker == "" {
		return v, usage("accept requires --worker or TSUZURI_WORKER")
	}
	v.coord = tsuzuri.Coordination{Command: command, ID: id, Owner: options["--owner"], Worker: worker, Worktree: options["--worktree"], ExpectedDigest: options["--expected-digest"], To: options["--to"], Checkpoint: options["--checkpoint"], NoWorktree: options["--no-worktree"] != ""}
	v.query = tsuzuri.Query{Command: command, ID: id, Source: options["--source"], Project: options["--project"], Kind: options["--kind"], State: options["--canonical-state"], ClaimStatus: options["--claim-status"], Format: options["--format"], Color: options["--color"], Refresh: options["--refresh"] != ""}
	return v, nil
}
