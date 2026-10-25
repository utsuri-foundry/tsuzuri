package tsuzuri

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type testRepository struct{ root, remote, clone string }

func isolatedGit(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("TSUZURI_WORKER", "")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
	}
	// Git interprets an empty GIT_DIR as a path rather than absence.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		if e := os.Unsetenv(key); e != nil {
			t.Fatal(e)
		}
	}
}
func checkedGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v\n%s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

const testConfig = `schema = 1
repository_id = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
task_root = "tasks/projects"
base_branch = "main"
claim_namespace = "tsuzuri/tasks"
worktree_root = ".."
worktree_template = "wt-{task_id}"
areas = ["core"]
risks = []
`
const testProject = `+++
schema = 1
id = "example"
title = "Example"
state = "active"
+++

# Goal

Coordinate work safely.

# Finish line

Tests pass.
`
const testTask = `+++
schema = 1
id = "first-task"
project = "example"
title = "First task"
kind = "delivery"
state = "ready"
priority = "p1"
blocked_by = []
areas = ["core"]
risks = []
created = 2026-10-24
+++

# Outcome

Useful change.

# Acceptance

Tests pass.
`

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(text), 0644); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) testRepository {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	clone := filepath.Join(root, "operator-a")
	checkedGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	checkedGit(t, root, "clone", remote, clone)
	configureClone(t, clone)
	writeTestFile(t, filepath.Join(clone, ".tsuzuri.toml"), testConfig)
	writeTestFile(t, filepath.Join(clone, "tasks/projects/example/project.md"), testProject)
	writeTestFile(t, filepath.Join(clone, "tasks/projects/example/tasks/first-task.task.md"), testTask)
	checkedGit(t, clone, "add", ".")
	checkedGit(t, clone, "-c", "commit.gpgSign=false", "commit", "-m", "fixture")
	checkedGit(t, clone, "push", "origin", "main")
	return testRepository{root, remote, clone}
}
func configureClone(t *testing.T, path string) {
	t.Helper()
	for _, p := range [][2]string{{"user.name", "Test"}, {"user.email", "test@invalid"}, {"commit.gpgSign", "false"}, {"tsuzuri.remote", "origin"}, {"tsuzuri.owner", "test-owner"}} {
		checkedGit(t, path, "config", "--local", p[0], p[1])
	}
}
func cloneFixture(t *testing.T, f testRepository, name string) string {
	t.Helper()
	path := filepath.Join(f.root, name)
	checkedGit(t, f.root, "clone", f.remote, path)
	configureClone(t, path)
	return path
}
func clientAt(t *testing.T, path string) *Client {
	t.Helper()
	c, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func mustClaim(t *testing.T, c *Client, worker string) ClaimResult {
	t.Helper()
	view, _, e := c.Show(context.Background(), "first-task")
	if e != nil {
		t.Fatal(e)
	}
	result, e := c.Coordinate(context.Background(), Coordination{Command: "claim", ID: "first-task", Worker: worker, ExpectedDigest: view.RecordDigest, NoWorktree: true})
	if e != nil {
		t.Fatal(e)
	}
	return result.Data.(ClaimResult)
}
func expectKind(t *testing.T, e error, kind string) {
	t.Helper()
	app, ok := e.(*Error)
	if !ok || app.Kind != kind {
		t.Fatalf("wanted %s; got %v", kind, e)
	}
}
func TestRecoveryRevisionAndCompletion(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	a := clientAt(t, f.clone)
	b := clientAt(t, cloneFixture(t, f, "operator-b"))
	claim := mustClaim(t, a, "worker/a")
	ctx := context.Background()
	checkedGit(t, f.clone, "update-ref", "-d", "refs/tsuzuri/receipts/"+claim.Metadata.RepositoryID+"/first-task")
	checkedGit(t, f.clone, "update-ref", "-d", "refs/tsuzuri/assignment-receipts/"+claim.Metadata.RepositoryID+"/first-task")
	_, e := a.Coordinate(ctx, Coordination{Command: "accept", ID: "first-task", Worker: "worker/b", NoWorktree: true})
	expectKind(t, e, "claim-conflict")
	accepted, e := a.Coordinate(ctx, Coordination{Command: "accept", ID: "first-task", Worker: "worker/a", NoWorktree: true})
	if e != nil {
		t.Fatal(e)
	}
	if accepted.Data.(AcceptanceResult).ClaimObjectID != claim.Metadata.AnchorObjectID {
		t.Fatal("recovery adopted different claim")
	}
	// Acceptance changes are visible without refreshing the original authorization.
	path := filepath.Join(f.clone, "tasks/projects/example/tasks/first-task.task.md")
	writeTestFile(t, path, strings.Replace(testTask, "Tests pass.", "New acceptance.", 1))
	checkedGit(t, f.clone, "add", ".")
	checkedGit(t, f.clone, "commit", "-m", "acceptance drift")
	checkedGit(t, f.clone, "push", "origin", "main")
	view, _, e := b.Show(ctx, "first-task")
	if e != nil {
		t.Fatal(e)
	}
	if view.ClaimedMarkdown == nil || *view.ClaimedMarkdown != testTask || view.Claim.ClaimedTask.RecordDigest != claim.ClaimedTask.RecordDigest || view.RecordDigest == claim.ClaimedTask.RecordDigest {
		t.Fatal("original task revision refreshed")
	}
	if !contains(view.Claim.Drift, "task-intent-changed") {
		t.Fatal("acceptance drift missing")
	}
	// A published implementation and terminal task are not canonical completion.
	work := filepath.Join(f.root, "implementation")
	checkedGit(t, f.clone, "worktree", "add", "-b", "implementation", work, claim.Metadata.AnchorObjectID)
	terminalDoc := strings.Replace(testTask, "state = \"ready\"", "state = \"done\"", 1) + "\n# Resolution\n\nImplemented.\n"
	writeTestFile(t, filepath.Join(work, "tasks/projects/example/tasks/first-task.task.md"), terminalDoc)
	checkedGit(t, work, "add", ".")
	checkedGit(t, work, "commit", "-m", "implementation")
	tip := checkedGit(t, work, "rev-parse", "HEAD")
	checkedGit(t, work, "push", "origin", "HEAD:"+claim.RemoteRef)
	checkedGit(t, work, "merge-base", "--is-ancestor", claim.Metadata.AnchorObjectID, tip)
	view, _, e = b.Show(ctx, "first-task")
	if e != nil {
		t.Fatal(e)
	}
	if view.State != "ready" || view.Claim.Status != "active" {
		t.Fatal("published branch treated as completion")
	}
	_, e = a.Coordinate(ctx, Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true, ExpectedDigest: claim.ClaimedTask.RecordDigest})
	expectKind(t, e, "lost-ownership")
	_, e = a.Coordinate(ctx, Coordination{Command: "release", ID: "first-task"})
	expectKind(t, e, "lost-ownership")
	// Canonical integration merges the metadata-only anchor ancestry as well.
	checkedGit(t, f.clone, "merge", "-s", "ours", "--no-edit", tip)
	writeTestFile(t, path, terminalDoc)
	checkedGit(t, f.clone, "add", ".")
	checkedGit(t, f.clone, "commit", "-m", "integrate terminal record")
	checkedGit(t, f.clone, "push", "origin", "main")
	view, _, e = b.Show(ctx, "first-task")
	if e != nil {
		t.Fatal(e)
	}
	if view.State != "done" || view.Claim.Status != "stale-terminal" {
		t.Fatal("canonical terminal record not recognized")
	}
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func TestRejectEffectivePushDestination(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	other := filepath.Join(f.root, "other.git")
	checkedGit(t, f.root, "init", "--bare", other)
	checkedGit(t, f.clone, "config", "--local", "url."+other+".pushInsteadOf", f.remote)
	c := clientAt(t, f.clone)
	_, e := c.Coordinate(context.Background(), Coordination{Command: "claim", ID: "first-task", NoWorktree: true})
	expectKind(t, e, "remote")
	if refs := checkedGit(t, other, "for-each-ref", "--format=%(refname)"); refs != "" {
		t.Fatal("mutated different destination", refs)
	}
	if refs := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname)", "refs/tsuzuri/"); refs != "" {
		t.Fatal("created receipts/snapshots before remote policy rejection", refs)
	}
}
func rustBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("TSUZURI_RUST_BINARY")
	if path == "" {
		t.Skip("set TSUZURI_RUST_BINARY to released v0.3.1 for mixed-client tests")
	}
	b, e := exec.Command(path, "--version").Output()
	if e != nil || strings.TrimSpace(string(b)) != "tsuzuri 0.3.1" {
		t.Fatalf("requires released Rust v0.3.1: %s %v", b, e)
	}
	return path
}
func rustCommand(binary, dir string, args ...string) (map[string]any, error) {
	c := exec.Command(binary, append([]string{"--json"}, args...)...)
	c.Dir = dir
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	var body map[string]any
	if parseErr := json.Unmarshal(b, &body); parseErr != nil {
		return nil, parseErr
	}
	return body, e
}
func TestRustGoInteroperability(t *testing.T) {
	binary := rustBinary(t)
	isolatedGit(t)
	for _, writer := range []string{"rust", "go"} {
		t.Run(writer+"-claims", func(t *testing.T) {
			f := fixture(t)
			b := cloneFixture(t, f, "operator-b")
			a := clientAt(t, f.clone)
			var claim ClaimResult
			if writer == "go" {
				claim = mustClaim(t, a, "worker/a")
			} else {
				body, e := rustCommand(binary, f.clone, "claim", "first-task", "--worker", "worker/a", "--no-worktree")
				if e != nil {
					t.Fatalf("Rust claim: %v %v", e, body)
				}
				data, _ := json.Marshal(body["data"])
				if e = json.Unmarshal(data, &claim); e != nil {
					t.Fatal(e)
				}
			}
			goView, _, e := clientAt(t, b).Show(context.Background(), "first-task")
			if e != nil {
				t.Fatal(e)
			}
			body, e := rustCommand(binary, b, "show", "first-task")
			if e != nil {
				t.Fatalf("Rust show: %v %v", e, body)
			}
			var rustView Inspection
			data, _ := json.Marshal(body["data"])
			if e = json.Unmarshal(data, &rustView); e != nil {
				t.Fatal(e)
			}
			for _, view := range []Inspection{goView, rustView} {
				if view.Claim == nil || view.Claim.Metadata.ClaimID != claim.Metadata.ClaimID || view.Claim.ClaimedTask.RecordDigest != claim.ClaimedTask.RecordDigest || view.Claim.Metadata.AnchorObjectID != claim.Metadata.AnchorObjectID || view.Worker == nil || *view.Worker != "worker/a" {
					t.Fatalf("different ownership/revision: %+v", view)
				}
			}
		})
	}
	for run := 0; run < 5; run++ {
		t.Run("mixed-claim-race", func(t *testing.T) {
			f := fixture(t)
			b := cloneFixture(t, f, "operator-b")
			c := clientAt(t, f.clone)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var goErr, rustErr error
			go func() {
				defer wg.Done()
				<-start
				_, goErr = c.Coordinate(context.Background(), Coordination{Command: "claim", ID: "first-task", Worker: "worker/go", NoWorktree: true})
			}()
			go func() {
				defer wg.Done()
				<-start
				_, rustErr = rustCommand(binary, b, "claim", "first-task", "--worker", "worker/rust", "--no-worktree")
			}()
			close(start)
			wg.Wait()
			if (goErr == nil) == (rustErr == nil) {
				t.Fatalf("one winner required: Go=%v Rust=%v", goErr, rustErr)
			}
			v, _, e := c.Show(context.Background(), "first-task")
			if e != nil {
				t.Fatal(e)
			}
			expected := "worker/go"
			if goErr != nil {
				expected = "worker/rust"
			}
			if v.Worker == nil || *v.Worker != expected {
				t.Fatal("reported conflicting ownership")
			}
		})
	}
}

func TestMixedAuthoringReceiptRecovery(t *testing.T) {
	binary := rustBinary(t)
	isolatedGit(t)
	for _, writer := range []string{"rust", "go"} {
		t.Run(writer+"-writes", func(t *testing.T) {
			f := fixture(t)
			work := filepath.Join(f.root, "authoring")
			checkedGit(t, f.clone, "worktree", "add", "-b", "authoring", work)
			c := clientAt(t, work)
			input := []byte(`{"id":"new-task","project":"example","title":"Literal \\u2028 and actual ` + "\u2028" + `","kind":"research","state":"ready","priority":"p2","blockedBy":[],"areas":["core"],"risks":[],"created":"2026-10-24","outcome":"A result.","acceptance":"Checks pass."}`)
			rustAuthor := func() (Result, error) {
				command := exec.Command(binary, "--json", "create-task", "--request-id", "mixed-authoring")
				command.Dir = work
				command.Stdin = bytes.NewReader(input)
				var stderr bytes.Buffer
				command.Stderr = &stderr
				b, e := command.Output()
				if e != nil {
					return Result{}, fmt.Errorf("Rust author: %w %s %s", e, b, stderr.String())
				}
				var result Result
				e = json.Unmarshal(b, &result)
				return result, e
			}
			goAuthor := func() (Result, error) {
				return c.Author(context.Background(), "create-task", "", "mixed-authoring", input)
			}
			create, recover := rustAuthor, goAuthor
			if writer == "go" {
				create, recover = goAuthor, rustAuthor
			}
			first, e := create()
			if e != nil {
				t.Fatal(e)
			}
			_ = first
			if _, e = recover(); e != nil {
				t.Fatal("cross-client completed replay", e)
			}
			// Lost materialization with a pending receipt must reproduce exactly the
			// digest that the first writer recorded, using the original request.
			path := filepath.Join(work, "tasks/projects/example/tasks/new-task.task.md")
			original, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			r, e := c.session(ctx)
			if e != nil {
				t.Fatal(e)
			}
			ref := "refs/tsuzuri/operations/0198d2de-18c7-7ca0-a327-e6b4760f8613/mixed-authoring"
			oid, receipt, e := r.readOperation(ctx, ref)
			if e != nil {
				t.Fatal(e)
			}
			receipt.Status = "pending"
			body, e := compactJSON(receipt)
			if e != nil {
				t.Fatal(e)
			}
			pending, e := r.blob(ctx, string(body), true)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.update(ctx, ref, pending, oid); e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if _, e = recover(); e != nil {
				t.Fatalf("pending cross-client creation: %v\noriginal document:\n%s", e, original)
			}
			after, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("pending recovery changed original bytes")
			}
		})
	}
}

func TestStaleClaimRetryCannotBypassAssignment(t *testing.T) {
	isolatedGit(t)
	for _, target := range []string{"worker/b", ""} {
		t.Run("handoff-to-"+target, func(t *testing.T) {
			f := fixture(t)
			a := clientAt(t, f.clone)
			b := clientAt(t, cloneFixture(t, f, "operator-b"))
			claim := mustClaim(t, a, "worker/a")
			ctx := context.Background()
			if _, e := b.Coordinate(ctx, Coordination{Command: "accept", ID: "first-task", Worker: "worker/a", NoWorktree: true}); e != nil {
				t.Fatal(e)
			}
			if _, e := b.Coordinate(ctx, Coordination{Command: "handoff", ID: "first-task", To: target, Checkpoint: claim.Metadata.AnchorObjectID}); e != nil {
				t.Fatal(e)
			}
			receipt := "refs/tsuzuri/assignment-receipts/" + claim.Metadata.RepositoryID + "/first-task"
			before := checkedGit(t, f.clone, "rev-parse", receipt)
			for _, worker := range []string{"", "worker/a"} {
				_, e := a.Coordinate(ctx, Coordination{Command: "claim", ID: "first-task", Worker: worker})
				expectKind(t, e, "lost-ownership")
			}
			_, e := a.Coordinate(ctx, Coordination{Command: "materialize", ID: "first-task"})
			expectKind(t, e, "lost-ownership")
			if after := checkedGit(t, f.clone, "rev-parse", receipt); after != before {
				t.Fatal("stale retry altered assignment receipt")
			}
			if refs := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname)", claim.RemoteRef); refs != "" {
				t.Fatal("stale retry created local implementation branch")
			}
			if _, e = os.Lstat(filepath.Join(f.root, "wt-first-task")); !os.IsNotExist(e) {
				t.Fatal("stale retry materialized worktree", e)
			}
		})
	}
}
