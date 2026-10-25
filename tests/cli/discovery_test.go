//go:build linux || darwin

package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatesAndSelectsReadyTasks(t *testing.T) {
	f := newFixture(t)
	check(t, f.ok(f.clone, "validate"), map[string]any{"schema": 1, "ok": true, "command": "validate", "data.tasks": 2})
	check(t, f.ok(f.clone, "next"), map[string]any{"data.0.id": "first-task", "data.0.kind": "delivery", "data.1.id": "second-task", "data.1.kind": "defect"})
}
func overviewRepository(f *fixture) {
	f.task("waiting-task", nonterminalTask("waiting-task", "tsuzuri", "ready", "p1", []string{"first-task"}, ""))
	f.task("backlog-task", nonterminalTask("backlog-task", "tsuzuri", "backlog", "p2", nil, ""))
	f.task("blocked-task", nonterminalTask("blocked-task", "tsuzuri", "blocked", "p1", nil, "Waiting for a product decision."))
	f.task("done-task", terminalTask("done-task", "p2", "done"))
	f.task("cancelled-task", terminalTask("cancelled-task", "p3", "cancelled"))
	write(f.t, filepath.Join(f.clone, "tasks/projects/paused/project.md"), "+++\nschema = 1\nid = \"paused\"\ntitle = \"Paused\"\nstate = \"paused\"\n+++\n\n# Goal\n\nPreserve paused work.\n\n# Finish line\n\nWork can resume.\n")
	write(f.t, filepath.Join(f.clone, "tasks/projects/paused/tasks/paused-task.task.md"), nonterminalTask("paused-task", "paused", "ready", "p2", nil, ""))
}
func TestOverviewReportsOneSnapshotAcrossEveryTaskSection(t *testing.T) {
	f := newFixture(t, overviewRepository)
	f.ok(f.clone, "claim", "first-task")
	v := f.ok(f.clone, "overview")
	check(t, v, map[string]any{"command": "overview", "source.kind": "remote-snapshot", "data.summary.totalTasks": 8, "data.summary.claimIssues": 0, "data.summary.staleTerminalClaims": 0, "data.claimableNow.0.id": "second-task", "data.activeClaims.0.id": "first-task", "data.activeClaims.0.claim.owner": "test-owner", "data.waitingOnDependencies.0.unfinishedDependencies.0": "first-task", "data.unavailable.0.projectAvailability": "paused"})
	for _, section := range []string{"claimableNow", "activeClaims", "waitingOnDependencies", "backlog", "blocked", "unavailable", "done", "cancelled"} {
		equal(t, field(t, v, "data.summary."+section), 1)
	}
	terminal := f.cli(f.clone, "overview", "--project", "tsuzuri", "--color", "never")
	success(t, terminal)
	for _, part := range []string{"claimable=1  active=1  waiting=1", "Waiting on dependencies (1)", "Priority  Task          Waiting for"} {
		contains(t, terminal.stdout, part, true)
	}
	contains(t, terminal.stdout, "\x1b[", false)
	contains(t, terminal.stdout, "paused-task", false)
	colored := f.cli(f.clone, "overview", "--project", "tsuzuri", "--color", "always")
	success(t, colored)
	for _, part := range []string{"\x1b[1mTsuzuri overview\x1b[0m", "\x1b[1;32mClaimable now (1)\x1b[0m", "\x1b[1;33mp1"} {
		contains(t, colored.stdout, part, true)
	}
	markdown := f.cli(f.clone, "overview", "--project", "tsuzuri", "--format", "markdown", "--color", "always")
	success(t, markdown)
	equal(t, strings.HasPrefix(markdown.stdout, "# Tsuzuri overview\n"), true)
	for _, part := range []string{"| Claimable | Active | Waiting |", "| `waiting-task` | first-task |", "| `first-task` | test-owner |"} {
		contains(t, markdown.stdout, part, true)
	}
	contains(t, markdown.stdout, "\x1b[", false)
}
func TestOverviewSurfacesInvalidClaimWithoutMarkingTaskClaimable(t *testing.T) {
	f := newFixture(t)
	f.git(f.clone, "push", "origin", "main:"+claimRef)
	v := f.ok(f.clone, "overview")
	check(t, v, map[string]any{"data.summary.claimableNow": 1, "data.summary.unavailable": 1, "data.summary.claimIssues": 1, "data.summary.staleTerminalClaims": 0, "data.unavailable.0.id": "first-task", "data.claimIssues.0.taskId": "first-task", "data.claimIssues.0.status": "invalid"})
	textField(t, v, "data.claimIssues.0.error")
}
func TestMalformedAssignmentIsolatedFromValidClaimOwnership(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	f.git(f.clone, "push", "origin", "main:"+assignmentRef)
	check(t, f.ok(f.clone, "status", "first-task"), map[string]any{"data.invalidClaims": 0, "data.invalidAssignments": 1, "data.claims.0.valid": true, "data.claims.0.assignment.valid": false})
	check(t, f.ok(f.clone, "overview"), map[string]any{"data.summary.activeClaims": 1, "data.summary.claimIssues": 1})
}
func TestOverviewSeparatesSafeTerminalClaimsFromClaimIssues(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	f.task("first-task", terminalTask("first-task", "p0", "done"))
	f.commitPush("complete claimed task")
	check(t, f.ok(f.clone, "overview"), map[string]any{"data.summary.claimIssues": 0, "data.summary.staleTerminalClaims": 1, "data.staleTerminalClaims.0.taskId": "first-task", "data.staleTerminalClaims.0.status": "stale-terminal"})
}
func TestDiscoversProjectsAndReportsCompatibility(t *testing.T) {
	f := newFixture(t)
	v := f.ok(f.clone, "compatibility")
	check(t, v, map[string]any{"data.jsonSchemaVersions.0": 1, "data.responseEnvelopeStream": "stdout", "data.assignmentProtocolVersions.0": 1})
	arrayContains(t, v, "data.commands", "overview")
	projects := f.ok(f.clone, "projects")
	check(t, projects, map[string]any{"source.kind": "remote-snapshot", "data.0.id": "tsuzuri", "data.0.goal": "Coordinate work safely.", "data.0.finishLine": "The protocol works."})
	textField(t, projects, "data.0.recordDigest")
	check(t, f.ok(f.clone, "project", "tsuzuri"), map[string]any{"data.path": "tasks/projects/tsuzuri/project.md", "source.repositoryId": repositoryID})
}
func TestJSONUsageFailuresUseTheStdoutEnvelope(t *testing.T) {
	f := newFixture(t)
	result := f.cli(f.clone, "--json", "not-a-command")
	jsonError(t, result, 2, "usage")
	equal(t, strings.HasPrefix(result.stderr, "error: "), true)
}
