//go:build linux || darwin

package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalHistoryRejectsReopenedTerminalTask(t *testing.T) {
	f := newFixture(t)
	f.task("first-task", terminalTask("first-task", "p0", "done"))
	f.commitPush("complete task")
	f.task("first-task", readyTask("first-task", "p0", "delivery"))
	f.commitPush("reopen task")
	r := f.cli(f.clone, "status")
	equal(t, r.code, 3)
	contains(t, r.stderr, "terminal task `first-task` changed state or path", true)
}
func TestMalformedTargetClaimBlocksOnlyItsTask(t *testing.T) {
	f := newFixture(t)
	f.git(f.clone, "push", "origin", "main:"+claimRef)
	jsonError(t, f.cli(f.clone, "--json", "claim", "first-task"), 5, "claim-conflict")
	check(t, f.ok(f.clone, "claim", "second-task", "--worktree", "wt-second"), map[string]any{"data.overlaps.0.incomplete": true, "data.overlaps.0.taskId": "first-task"})
}
func TestRejectsDistinctPushDestinationBeforeRemoteStateRead(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.root, "other.git")
	f.git(f.root, "init", "--bare", other)
	f.git(f.clone, "config", "--local", "remote.origin.pushurl", other)
	jsonError(t, f.cli(f.clone, "--json", "claim", "first-task"), 6, "remote")
	if f.run(f.remote, "", "git", "show-ref", "--verify", claimRef).code == 0 {
		t.Fatal("claim was written despite push destination refusal")
	}
}
func TestAmbiguousAcceptedPushRetainsOneReceiptAndNoWorktree(t *testing.T) {
	f := newFixture(t)
	offline := filepath.Join(f.root, "coordination-offline.git")
	f.hook("post-receive", "#!/bin/sh\nmv "+quote(f.remote)+" "+quote(offline)+"\n")
	v := jsonError(t, f.cli(f.clone, "--json", "claim", "first-task"), 7, "indeterminate")
	equal(t, field(t, v, "error.details.taskId"), "first-task")
	textField(t, v, "error.details.claimId")
	textField(t, v, "error.details.safeAction")
	pathType(t, offline, true)
	destination := filepath.Join(f.root, "wt-first-task")
	exists(t, destination, false)
	f.git(f.clone, "rev-parse", "--verify", "refs/tsuzuri/receipts/"+repositoryID+"/first-task^{object}")
	f.git(offline, "show-ref", "--verify", claimRef)
	rename(t, offline, f.remote)
	f.ok(f.clone, "materialize", "first-task")
	pathType(t, destination, true)
}
func TestConcurrentCommitAfterRemoteReleaseIsPreserved(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	destination := filepath.Join(f.root, "wt-first-task")
	f.hook("post-receive", "#!/bin/sh\nunset GIT_DIR\ngit -C "+quote(destination)+" commit --allow-empty -m 'concurrent local commit'\n")
	v := jsonError(t, f.cli(f.clone, "--json", "release", "first-task"), 8, "preserved-work")
	check(t, v, map[string]any{"error.details.taskId": "first-task", "error.details.knownEffects.0": "remote claim ref is absent"})
	pathType(t, destination, true)
	f.git(f.clone, "show-ref", "--verify", claimRef)
	if f.run(f.remote, "", "git", "show-ref", "--verify", claimRef).code == 0 {
		t.Fatal("remote claim still exists")
	}
}
func TestResponseLostReleaseResumesAfterConfirmedAbsence(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	destination := filepath.Join(f.root, "wt-first-task")
	offline := filepath.Join(f.root, "coordination-release-offline.git")
	f.hook("post-receive", "#!/bin/sh\nmv "+quote(f.remote)+" "+quote(offline)+"\n")
	v := jsonError(t, f.cli(f.clone, "--json", "release", "first-task"), 7, "indeterminate")
	equal(t, field(t, v, "error.details.uncertainty"), "the conditional deletion may or may not have been accepted")
	pathType(t, destination, true)
	if f.run(offline, "", "git", "show-ref", "--verify", claimRef).code == 0 {
		t.Fatal("remote deletion did not happen")
	}
	rename(t, offline, f.remote)
	equal(t, field(t, f.ok(f.clone, "release", "first-task"), "data.remoteAlreadyAbsent"), true)
	exists(t, destination, false)
}
func TestStatusClassifiesCompatibleBaseAdvancement(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	f.git(f.clone, "commit", "--allow-empty", "-m", "advance base")
	f.git(f.clone, "push", "origin", "main")
	arrayContains(t, f.ok(f.clone, "status", "first-task"), "data.claims.0.drift", "compatible-base-advancement")
}
func TestRewrittenClaimBaseIsValidatedIndependently(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	tree := f.git(f.clone, "rev-parse", "HEAD^{tree}")
	rewritten := f.git(f.clone, "-c", "commit.gpgSign=false", "commit-tree", tree, "-m", "rewrite canonical history")
	f.git(f.clone, "update-ref", "refs/heads/main", rewritten)
	f.git(f.clone, "push", "--force", "origin", "main")
	v := f.ok(f.clone, "status", "first-task")
	equal(t, field(t, v, "data.claims.0.valid"), true)
	arrayContains(t, v, "data.claims.0.drift", "history-rewritten")
}
func TestStatusClassifiesCoordinationConfigurationDrift(t *testing.T) {
	f := newFixture(t)
	f.ok(f.clone, "claim", "first-task")
	path := filepath.Join(f.clone, ".tsuzuri.toml")
	write(t, path, strings.ReplaceAll(read(t, path), "areas = [\"core\"]", "areas = [\"core\", \"docs\"]"))
	f.commitPush("change coordination configuration")
	arrayContains(t, f.ok(f.clone, "status", "first-task"), "data.claims.0.drift", "coordination-configuration-changed")
}
func TestRemoteCommandsPreservePreKindHistoryAndClaims(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		for _, id := range []string{"first-task", "second-task"} {
			path := filepath.Join(f.clone, taskRoot, id+".task.md")
			s := read(t, path)
			s = strings.ReplaceAll(s, "kind = \"delivery\"\n", "")
			s = strings.ReplaceAll(s, "kind = \"defect\"\n", "")
			write(t, path, s)
		}
	})
	base := f.git(f.clone, "rev-parse", "HEAD")
	anchor := f.createClaim(base, "first-task", 0)
	f.git(f.clone, "push", "origin", anchor+":"+claimRef)
	f.writeRepository()
	f.commitPush("classify task kinds")
	v := f.ok(f.clone, "list", "--kind", "delivery")
	arrayLength(t, v, "data", 1)
	equal(t, field(t, v, "data.0.id"), "first-task")
	status := f.ok(f.clone, "status", "first-task")
	equal(t, field(t, status, "data.claims.0.status"), "active")
	arrayContains(t, status, "data.claims.0.drift", "task-intent-changed")
}
