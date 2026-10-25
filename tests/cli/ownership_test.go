//go:build linux || darwin

package cli_test

import (
	"path/filepath"
	"testing"
)

func TestClaimsMaterializesAndReleasesUnusedWork(t *testing.T) {
	f := newFixture(t)
	destination := filepath.Join(f.root, "wt-first-task")
	f.ok(f.clone, "claim", "first-task")
	pathType(t, destination, true)
	f.git(f.remote, "show-ref", "--verify", claimRef)
	check(t, f.ok(f.clone, "status", "first-task"), map[string]any{"data.claims.0.status": "active", "data.claims.0.metadata.owner": "test-owner", "data.claims.0.assignment": nil})
	f.ok(f.clone, "release", "first-task")
	exists(t, destination, false)
	if f.run(f.remote, "", "git", "show-ref", "--verify", claimRef).code == 0 {
		t.Fatal("remote claim survived release")
	}
	arrayLength(t, f.ok(f.clone, "status"), "data.claims", 0)
}
func TestWorkerAssignmentsSupportDirectAndOpenHandoffs(t *testing.T) {
	f := newFixture(t)
	check(t, f.ok(f.clone, "claim", "first-task", "--worker", "codex/session-a"), map[string]any{"data.assignment.state": "assigned", "data.assignment.worker": "codex/session-a"})
	check(t, f.ok(f.clone, "handoff", "first-task", "--to", "codex/session-b"), map[string]any{"data.assignment.worker": "codex/session-b", "data.removedLocalReceipt": true, "data.removedLocalAssignmentReceipt": true})
	equal(t, field(t, f.ok(f.clone, "accept", "first-task", "--worker", "codex/session-b"), "data.updatedLocalAssignmentReceipt"), true)
	check(t, f.ok(f.clone, "handoff", "first-task"), map[string]any{"data.assignment.state": "handoff-pending", "data.assignment.worker": nil})
	f.ok(f.clone, "accept", "first-task", "--worker", "codex/session-c")
	check(t, f.ok(f.clone, "status", "first-task"), map[string]any{"data.claims.0.assignment.metadata.worker": "codex/session-c", "data.invalidAssignments": 0})
	check(t, f.ok(f.clone, "show", "first-task"), map[string]any{"data.owner": "test-owner", "data.worker": "codex/session-c", "data.assignmentStatus": "assigned"})
	check(t, f.ok(f.clone, "release", "first-task"), map[string]any{"data.removedRemoteAssignment": true, "data.removedAssignmentReceipt": true})
	if f.run(f.remote, "", "git", "show-ref", "--verify", assignmentRef).code == 0 {
		t.Fatal("remote assignment survived release")
	}
}
func TestStaleWorkerReceiptCannotInitiateALaterHandoff(t *testing.T) {
	f := newFixture(t)
	stale := textField(t, f.ok(f.clone, "claim", "first-task", "--worker", "codex/session-a"), "data.assignment.objectId")
	f.ok(f.clone, "handoff", "first-task", "--to", "codex/session-b")
	f.ok(f.clone, "accept", "first-task", "--worker", "codex/session-b")
	f.git(f.clone, "update-ref", "refs/tsuzuri/assignment-receipts/"+repositoryID+"/first-task", stale)
	for _, args := range [][]string{{"--json", "handoff", "first-task", "--to", "codex/session-c"}, {"--json", "materialize", "first-task"}, {"--json", "release", "first-task"}} {
		jsonError(t, f.cli(f.clone, args...), 5, "lost-ownership")
	}
	equal(t, field(t, f.ok(f.clone, "status", "first-task"), "data.claims.0.assignment.metadata.worker"), "codex/session-b")
}
func TestWorkerCanBeSuppliedByAnAgentEnvironment(t *testing.T) {
	f := newFixture(t)
	r := execute(t.Context(), f.clone, f.envWith(map[string]string{"TSUZURI_WORKER": "codex/environment-session"}), "", binary, "--json", "claim", "first-task")
	success(t, r)
	equal(t, field(t, body(t, r), "data.assignment.worker"), "codex/environment-session")
}
func TestHandoffRefusesADirtyCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task", "--worker", "codex/session-a")
	path := filepath.Join(f.root, "wt-first-task", "uncommitted.txt")
	write(t, path, "preserve me\n")
	jsonError(t, f.cli(f.clone, "--json", "handoff", "first-task", "--to", "codex/session-b"), 8, "preserved-work")
	pathType(t, path, false)
}
func TestCrossCloneHandoffMaterializesTheExactRemoteCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task", "--worker", "codex/session-a")
	outgoing := filepath.Join(f.root, "wt-first-task")
	write(t, filepath.Join(outgoing, "checkpoint.txt"), "handoff checkpoint\n")
	f.git(outgoing, "add", "checkpoint.txt")
	f.git(outgoing, "commit", "-m", "checkpoint implementation")
	f.git(outgoing, "push", "origin", "HEAD:"+claimRef)
	checkpoint := f.git(outgoing, "rev-parse", "HEAD")
	f.ok(f.clone, "handoff", "first-task")
	recipient := f.independentClone()
	f.configure(recipient, "test-owner")
	f.ok(recipient, "accept", "first-task", "--worker", "codex/session-b", "--worktree", "wt-recipient-first-task")
	incoming := filepath.Join(f.root, "wt-recipient-first-task")
	equal(t, read(t, filepath.Join(incoming, "checkpoint.txt")), "handoff checkpoint\n")
	equal(t, f.git(incoming, "rev-parse", "HEAD"), checkpoint)
}

func (f *fixture) concurrent(directories []string, arguments [][]string) []output {
	start := make(chan struct{})
	results := make(chan output, len(directories))
	for i, dir := range directories {
		args := arguments[i]
		go func() { <-start; results <- execute(f.t.Context(), dir, f.env, "", binary, args...) }()
	}
	close(start)
	outputs := make([]output, 0, len(directories))
	for range directories {
		outputs = append(outputs, <-results)
	}
	return outputs
}
func oneWinner(t *testing.T, outputs []output) output {
	t.Helper()
	wins := 0
	var loser output
	for _, result := range outputs {
		if result.code == 0 {
			success(t, result)
			wins++
		} else {
			loser = result
		}
	}
	equal(t, wins, 1)
	equal(t, loser.code, 5)
	return loser
}
func TestConcurrentHandoffAcceptanceHasOneWinner(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task", "--worker", "codex/session-a")
	f.ok(f.clone, "handoff", "first-task")
	oneWinner(t, f.concurrent([]string{f.clone, f.clone}, [][]string{{"--json", "accept", "first-task", "--worker", "codex/session-b"}, {"--json", "accept", "first-task", "--worker", "claude/session-c"}}))
	worker := textField(t, f.ok(f.clone, "status", "first-task"), "data.claims.0.assignment.metadata.worker")
	if worker != "codex/session-b" && worker != "claude/session-c" {
		t.Fatalf("unexpected worker %q", worker)
	}
}
func TestReleasePreservesImplementationCommits(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	dir := filepath.Join(f.root, "wt-first-task")
	write(t, filepath.Join(dir, "implementation.txt"), "useful work\n")
	f.git(dir, "add", "implementation.txt")
	f.git(dir, "commit", "-m", "implement task")
	jsonError(t, f.cli(f.clone, "--json", "release", "first-task"), 8, "preserved-work")
	pathType(t, dir, true)
	f.git(f.clone, "show-ref", "--verify", claimRef)
}
func TestInterruptedMaterializationResumesFromReceipt(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.root, "occupied")
	path := filepath.Join(dir, "keep.txt")
	write(t, path, "not a worktree\n")
	v := jsonError(t, f.cli(f.clone, "--json", "claim", "first-task", "--worktree", "occupied"), 8, "local-materialization")
	equal(t, field(t, v, "error.details.taskId"), "first-task")
	textField(t, v, "error.details.anchorObjectId")
	textField(t, v, "error.details.safeAction")
	pathType(t, path, false)
	f.git(f.remote, "show-ref", "--verify", claimRef)
	remove(t, path)
	remove(t, dir)
	f.ok(f.clone, "materialize", "first-task", "--worktree", "occupied")
	pathType(t, filepath.Join(dir, ".git"), false)
}
func TestConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	f := newFixture(t)
	second := f.independentClone()
	oneWinner(t, f.concurrent([]string{f.clone, second}, [][]string{{"claim", "first-task", "--worktree", "winner-a"}, {"claim", "first-task", "--worktree", "winner-b"}}))
}
