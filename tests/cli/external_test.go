//go:build linux || darwin

package cli_test

import (
	"path/filepath"
	"testing"
)

func TestExternalCoordinatorLifecycleSurvivesRestartAndCrossCloneHandoff(t *testing.T) {
	f := newFixture(t)
	second := f.independentClone()
	inspected := f.ok(f.clone, "show", "first-task")
	digest := textField(t, inspected, "data.recordDigest")
	args := []string{"claim", "first-task", "--no-worktree", "--worker", "ao/workflow-1", "--expected-digest", digest}
	claimed := f.ok(f.clone, args...)
	anchor := textField(t, claimed, "data.metadata.anchorObjectId")
	check(t, claimed, map[string]any{"data.claimedTask.recordDigest": digest, "data.materialization": nil})
	f.noWorkspace(f.clone)
	restarted := f.ok(f.clone, args...)
	equal(t, field(t, claimed, "data.metadata"), field(t, restarted, "data.metadata"))
	equal(t, field(t, claimed, "data.assignment"), field(t, restarted, "data.assignment"))
	f.noWorkspace(f.clone)
	jsonError(t, f.cli(second, "--json", "claim", "first-task", "--no-worktree"), 5, "claim-conflict")
	f.noWorkspace(second)
	observed := f.ok(second, "show", "first-task")
	equal(t, field(t, observed, "data.claim.metadata"), field(t, claimed, "data.metadata"))
	equal(t, field(t, observed, "data.claimedMarkdown"), field(t, inspected, "data.markdown"))
	f.git(second, "checkout", "-b", "ao/attempt-1", anchor)
	write(t, filepath.Join(second, "implementation.txt"), "reviewed candidate\n")
	write(t, filepath.Join(second, taskRoot, "first-task.task.md"), terminalTask("first-task", "p0", "done"))
	f.git(second, "add", ".")
	f.git(second, "commit", "-m", "implement and record completion")
	candidate := f.git(second, "rev-parse", "HEAD")
	f.git(second, "push", "origin", candidate+":"+claimRef)
	check(t, f.ok(f.clone, "show", "first-task"), map[string]any{"data.state": "ready", "data.claimStatus": "active"})
	jsonError(t, f.cli(f.clone, "--json", "handoff", "first-task", "--checkpoint", anchor), 5, "stale-input")
	f.ok(f.clone, "handoff", "first-task", "--checkpoint", candidate, "--to", "ao/workflow-2")
	accepted := f.ok(second, "accept", "first-task", "--worker", "ao/workflow-2", "--no-worktree")
	check(t, accepted, map[string]any{"data.claimId": field(t, claimed, "data.metadata.claimId"), "data.claimObjectId": candidate, "data.claimedTask": field(t, claimed, "data.claimedTask"), "data.materialization": nil})
	f.noWorkspace(second)
	resumed := f.ok(second, "accept", "first-task", "--worker", "ao/workflow-2", "--no-worktree")
	equal(t, field(t, resumed, "data.assignment"), field(t, accepted, "data.assignment"))
	jsonError(t, f.cli(f.clone, "--json", "accept", "first-task", "--worker", "ao/workflow-1", "--no-worktree"), 5, "claim-conflict")
	jsonError(t, f.cli(second, "--json", "release", "first-task"), 5, "lost-ownership")
	exists(t, filepath.Join(second, "implementation.txt"), true)
	f.ok(second, "handoff", "first-task", "--checkpoint", candidate)
	equal(t, field(t, f.ok(f.clone, "show", "first-task"), "data.assignmentStatus"), "handoff-pending")
	next := f.ok(f.clone, "accept", "first-task", "--worker", "ao/workflow-3", "--no-worktree")
	equal(t, field(t, next, "data.claimId"), field(t, claimed, "data.metadata.claimId"))
	f.noWorkspace(f.clone)
	f.git(f.clone, "merge", "--ff-only", candidate)
	f.git(f.clone, "push", "origin", candidate+":refs/heads/main")
	for _, dir := range []string{f.clone, second} {
		check(t, f.ok(dir, "show", "first-task"), map[string]any{"data.state": "done", "data.claimStatus": "stale-terminal", "data.claim.claimedTask.recordDigest": digest, "data.claimedMarkdown": field(t, inspected, "data.markdown")})
	}
}
func TestExternalClaimPinsProseAndRejectsStaleAuthorization(t *testing.T) {
	f := newFixture(t)
	before := f.ok(f.clone, "show", "first-task")
	digest := textField(t, before, "data.recordDigest")
	jsonError(t, f.cli(f.clone, "--json", "claim", "first-task", "--no-worktree", "--expected-digest", "wrong"), 5, "stale-input")
	equal(t, f.git(f.clone, "for-each-ref", "--format=%(refname)", "refs/tsuzuri/receipts"), "")
	equal(t, field(t, f.ok(f.clone, "show", "first-task"), "data.claimStatus"), "absent")
	claimed := f.ok(f.clone, "claim", "first-task", "--no-worktree", "--expected-digest", digest)
	path := filepath.Join(f.clone, taskRoot, "first-task.task.md")
	original := read(t, path)
	write(t, path, original+"\nAdditional acceptance requirement.\n")
	f.commitPush("change acceptance prose only")
	after := f.ok(f.clone, "show", "first-task")
	newDigest := textField(t, after, "data.recordDigest")
	if newDigest == digest {
		t.Fatal("acceptance change did not change digest")
	}
	equal(t, field(t, after, "data.claimedMarkdown"), original)
	arrayContains(t, after, "data.claim.drift", "task-intent-changed")
	replay := f.ok(f.clone, "claim", "first-task", "--no-worktree", "--expected-digest", digest)
	equal(t, field(t, replay, "data.claimedTask"), field(t, claimed, "data.claimedTask"))
	jsonError(t, f.cli(f.clone, "--json", "claim", "first-task", "--no-worktree", "--expected-digest", newDigest), 5, "stale-input")
	check(t, f.ok(f.clone, "release", "first-task"), map[string]any{"data.removedLocalBranch": false, "data.removedWorktree": nil})
}
func TestWorkspaceFreeFlagsPreserveManagedWorkspaceGuards(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"--json", "claim", "first-task", "--no-worktree", "--worktree", "unused"}, {"--json", "accept", "first-task", "--worker", "ao/a", "--no-worktree", "--worktree", "unused"}} {
		jsonError(t, f.cli(f.clone, args...), 2, "usage")
	}
	claim := f.ok(f.clone, "claim", "first-task", "--no-worktree", "--worker", "ao/a")
	anchor := textField(t, claim, "data.metadata.anchorObjectId")
	path := textField(t, f.ok(f.clone, "materialize", "first-task"), "data.worktreePath")
	file := filepath.Join(path, "unpublished.txt")
	write(t, file, "preserve\n")
	jsonError(t, f.cli(f.clone, "--json", "handoff", "first-task", "--checkpoint", anchor, "--to", "ao/b"), 8, "preserved-work")
	exists(t, file, true)
}
func TestConcurrentWorkspaceFreeClaimsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	second := f.independentClone()
	args := []string{"--json", "claim", "first-task", "--no-worktree"}
	loser := oneWinner(t, f.concurrent([]string{f.clone, second}, [][]string{args, args}))
	kind := textField(t, body(t, loser), "error.kind")
	if kind != "claim-conflict" && kind != "lost-ownership" {
		t.Fatalf("unexpected loser: %s", loser.stdout)
	}
	f.noWorkspace(f.clone)
	f.noWorkspace(second)
}
func TestWorkspaceFreeClaimRecoversAnAmbiguousRemoteWrite(t *testing.T) {
	f := newFixture(t)
	offline := filepath.Join(f.root, "offline.git")
	f.hook("post-receive", "#!/bin/sh\nmv "+quote(f.remote)+" "+quote(offline)+"\n")
	args := []string{"--json", "claim", "first-task", "--no-worktree", "--worker", "ao/durable-workflow"}
	lost := jsonError(t, f.cli(f.clone, args...), 7, "indeterminate")
	check(t, lost, map[string]any{"error.details.request.noWorktree": true, "error.details.request.worker": "ao/durable-workflow"})
	f.noWorkspace(f.clone)
	rename(t, offline, f.remote)
	remove(t, filepath.Join(f.remote, "hooks/post-receive"))
	r := f.cli(f.clone, args...)
	success(t, r)
	check(t, body(t, r), map[string]any{"data.metadata.claimId": field(t, lost, "error.details.claimId"), "data.assignment.worker": "ao/durable-workflow"})
	f.noWorkspace(f.clone)
	f.ok(f.clone, "release", "first-task")
}
