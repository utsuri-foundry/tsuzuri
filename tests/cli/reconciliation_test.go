//go:build linux || darwin

package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptReconcilesAssignmentBeforeIssuingReceipts(t *testing.T) {
	reconciliationCases(t, []string{"worker", "pending", "absent", "unreadable", "malformed"}, false)
}
func TestAcceptReconciliationPreservesExistingReceipts(t *testing.T) {
	reconciliationCases(t, []string{"worker", "unreadable"}, true)
}
func TestAcceptUnchangedGenerationReconcilesAndReplays(t *testing.T) {
	reconciliationCases(t, []string{"unchanged"}, false)
}
func reconciliationCases(t *testing.T, faults []string, existing bool) {
	for _, pending := range []bool{false, true} {
		for _, noWorktree := range []bool{false, true} {
			for _, fault := range faults {
				t.Run(fmt.Sprintf("pending=%t/no-worktree=%t/%s", pending, noWorktree, fault), func(t *testing.T) { checkAcceptReconciliation(t, pending, noWorktree, fault, existing) })
			}
		}
	}
}
func checkAcceptReconciliation(t *testing.T, pending, noWorktree bool, fault string, existingReceipts bool) {
	f := newFixture(t)
	second := f.independentClone()
	claim := f.ok(f.clone, "claim", "first-task", "--worker", "worker-a", "--no-worktree")
	anchor := textField(t, claim, "data.metadata.anchorObjectId")
	claimID := textField(t, claim, "data.metadata.claimId")
	if existingReceipts {
		f.ok(second, "accept", "first-task", "--worker", "worker-a", "--no-worktree")
		write(t, filepath.Join(second, "unpublished.txt"), "preserve external work\n")
	}
	if pending {
		f.ok(f.clone, "handoff", "first-task", "--checkpoint", anchor)
	}
	receiptArgs := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts"}
	before := f.git(second, receiptArgs...)
	equal(t, before != "", existingReceipts)
	wrapperDir := filepath.Join(f.root, "injected-bin")
	executable(t, filepath.Join(wrapperDir, "git"), read(t, "testdata/accept-reconciliation-git.sh"))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "accept", "first-task", "--worker", "worker-a"}
	if noWorktree {
		args = append(args, "--no-worktree")
	}
	env := f.envWith(map[string]string{"PATH": wrapperDir + string(os.PathListSeparator) + os.Getenv("PATH"), "TSUZURI_TEST_GIT": realGit, "TSUZURI_TEST_ROOT": f.root, "TSUZURI_TEST_REMOTE": f.remote, "TSUZURI_TEST_FAULT": fault, "TSUZURI_TEST_REPOSITORY": repositoryID, "TSUZURI_TEST_CLAIM": claimID})
	result := execute(t.Context(), second, env, "", binary, args...)
	selected := strings.TrimSpace(read(t, filepath.Join(f.root, "selected")))
	pathType(t, filepath.Join(f.root, "final-assignment-read"), false)
	equal(t, f.git(f.remote, "rev-parse", claimRef), anchor)
	actual := f.git(f.remote, "for-each-ref", "--format=%(objectname)", assignmentRef)
	expected := selected
	switch fault {
	case "worker", "pending":
		expected = strings.TrimSpace(read(t, filepath.Join(f.root, "successor")))
	case "absent":
		expected = ""
	}
	equal(t, actual, expected)
	v := body(t, result)
	if fault == "unchanged" {
		success(t, result)
		equal(t, field(t, v, "data.assignment.objectId"), selected)
		replay := f.cli(second, args...)
		success(t, replay)
		check(t, body(t, replay), map[string]any{"data.assignment": field(t, v, "data.assignment"), "data.createdLocalReceipt": false, "data.updatedLocalAssignmentReceipt": false})
		if noWorktree {
			f.noWorkspace(second)
		}
		return
	}
	code, kind := 5, "lost-ownership"
	if fault == "unreadable" || fault == "malformed" {
		code, kind = 7, "indeterminate"
	}
	jsonError(t, result, code, kind)
	check(t, v, map[string]any{"error.details.expectedObjectId": selected, "error.details.remoteRef": assignmentRef, "error.details.receiptWritesStarted": false})
	if code == 5 {
		var expected any
		if actual != "" {
			expected = actual
		}
		equal(t, field(t, v, "error.details.actualObjectId"), expected)
	} else if fault == "unreadable" {
		contains(t, textField(t, v, "error.message"), "injected exact assignment read failure", true)
	}
	equal(t, f.git(second, receiptArgs...), before)
	f.noWorkspace(second)
	exists(t, filepath.Join(f.root, "wt-first-task"), false)
	if existingReceipts {
		equal(t, read(t, filepath.Join(second, "unpublished.txt")), "preserve external work\n")
	}
	if fault == "worker" || fault == "pending" {
		check(t, f.ok(second, "status", "first-task"), map[string]any{"data.claims.0.assignment.valid": true, "data.claims.0.assignment.metadata.previousObjectId": selected})
	}
}
