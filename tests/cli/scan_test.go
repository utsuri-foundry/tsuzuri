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

func (f *fixture) createClaim(base, task string, index int) string {
	tree := f.git(f.clone, "rev-parse", base+"^{tree}")
	message := fmt.Sprintf("tsuzuri claim %s\n\nTsuzuri-Claim-Version: 1\nTsuzuri-Repository: %s\nTsuzuri-Task: %s\nTsuzuri-Claim: 00000000-0000-4000-8000-%012x\nTsuzuri-Owner: test-owner-%d\nTsuzuri-Base: %s\nTsuzuri-Claimed-At: 2026-08-21T12:00:00Z\n", task, repositoryID, task, index, index, base)
	return f.datedCommit(tree, base, message)
}
func (f *fixture) datedCommit(tree, parent, message string) string {
	env := f.envWith(map[string]string{"GIT_AUTHOR_DATE": "2026-08-21T12:00:00Z", "GIT_COMMITTER_DATE": "2026-08-21T12:00:00Z"})
	r := execute(f.t.Context(), f.clone, env, message, "git", "-c", "commit.gpgSign=false", "commit-tree", tree, "-p", parent)
	success(f.t, r)
	return strings.TrimSpace(r.stdout)
}
func (f *fixture) assignmentChain(anchor, task, claimID string, count int) string {
	tree := f.git(f.clone, "rev-parse", anchor+"^{tree}")
	previous := anchor
	for i := 0; i < count; i++ {
		message := fmt.Sprintf("tsuzuri assignment %s\n\nTsuzuri-Assignment-Version: 1\nTsuzuri-Repository: %s\nTsuzuri-Task: %s\nTsuzuri-Claim: %s\nTsuzuri-Assignment: 10000000-0000-4000-8000-%012x\nTsuzuri-State: assigned\nTsuzuri-Worker: codex/performance\nTsuzuri-Previous: %s\nTsuzuri-Updated-At: 2026-08-21T12:00:00Z\n", task, repositoryID, task, claimID, i, previous)
		previous = f.datedCommit(tree, previous, message)
	}
	return previous
}
func TestRemoteInventoryValidationIsBoundedPerSnapshot(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		for i := 0; i < 28; i++ {
			id := fmt.Sprintf("performance-task-%02d", i)
			f.task(id, readyTask(id, "p2", "maintenance"))
		}
	})
	base := f.git(f.clone, "rev-parse", "HEAD")
	args := []string{"push", "origin"}
	first := ""
	for i := 0; i < 15; i++ {
		task := fmt.Sprintf("performance-task-%02d", i)
		anchor := f.createClaim(base, task, i)
		if i == 0 {
			first = anchor
		}
		args = append(args, anchor+":refs/heads/tsuzuri/tasks/"+task)
	}
	f.git(f.clone, args...)
	tip := f.assignmentChain(first, "performance-task-00", "00000000-0000-4000-8000-000000000000", 64)
	f.git(f.clone, "push", "origin", tip+":refs/heads/tsuzuri/tasks-assignments/performance-task-00")
	for i := 0; i < 6; i++ {
		f.task("performance-task-27", strings.ReplaceAll(readyTask("performance-task-27", "p2", "maintenance"), "title = \"performance-task-27\"", fmt.Sprintf("title = \"Performance revision %d\"", i)))
		f.commitPush(fmt.Sprintf("advance task intent %d", i))
	}
	for i := 0; i < 12; i++ {
		f.git(f.clone, "commit", "--allow-empty", "-m", fmt.Sprintf("unrelated history %d", i))
	}
	f.git(f.clone, "push", "origin", "main")
	counter := filepath.Join(f.root, "git-invocations")
	wrapperDir := filepath.Join(f.root, "instrumented-bin")
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	executable(t, filepath.Join(wrapperDir, "git"), "#!/bin/sh\nwhile [ \"$1\" = -c ]; do case \"$2\" in core.hooksPath=/dev/null|protocol.ext.allow=never) shift 2 ;; *) break ;; esac; done\nprintf '%s\\n' \"$1\" >> \"$TSUZURI_GIT_COUNTER\"\nexec "+quote(gitBinary)+" -c core.hooksPath=/dev/null -c protocol.ext.allow=never \"$@\"\n")
	env := f.envWith(map[string]string{"PATH": wrapperDir + string(os.PathListSeparator) + os.Getenv("PATH"), "TSUZURI_GIT_COUNTER": counter})
	result := execute(t.Context(), f.clone, env, "", binary, "--json", "next")
	success(t, result)
	arrayLength(t, body(t, result), "data", 15)
	invocations := strings.Fields(read(t, counter))
	counts := map[string]int{}
	for _, command := range invocations {
		counts[command]++
	}
	equal(t, counts["rev-list"], 3)
	for command, maximum := range map[string]int{"ls-tree": 10, "cat-file": 11, "show": 100} {
		if counts[command] > maximum {
			t.Fatalf("%s: %d > %d\n%v", command, counts[command], maximum, invocations)
		}
	}
	if len(invocations) > 305 {
		t.Fatalf("%d subprocesses > 305\n%v", len(invocations), invocations)
	}
}
