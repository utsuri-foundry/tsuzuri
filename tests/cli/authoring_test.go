//go:build linux || darwin

package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (f *fixture) pendingReceipt(dir, requestID string) string {
	ref := "refs/tsuzuri/operations/" + repositoryID + "/" + requestID
	completed := f.git(dir, "rev-parse", "--verify", ref)
	var receipt map[string]any
	if err := json.Unmarshal([]byte(f.git(dir, "cat-file", "blob", completed)), &receipt); err != nil {
		f.t.Fatal(err)
	}
	receipt["status"] = "pending"
	pending := f.gitInput(dir, encode(f.t, receipt), "hash-object", "-w", "--stdin")
	f.git(dir, "update-ref", ref, pending, completed)
	return pending
}
func TestAuthoringAndTransitionsAreIdempotentAndGuarded(t *testing.T) {
	f := newFixture(t)
	authoring := f.worktree("automation-authoring")
	alias := filepath.Join(f.root, "authoring-alias")
	if err := os.Symlink(authoring, alias); err != nil {
		t.Fatal(err)
	}
	authoring = alias
	input := `{"id":"coordinator-task","project":"tsuzuri","title":"Coordinator task","kind":"delivery","state":"ready","priority":"p1","blockedBy":[],"areas":["core"],"risks":["coordination"],"created":"2026-09-26","blockedReason":null,"outcome":"A coordinator can create work.","acceptance":"- Creation is idempotent.","resolution":null}`
	jsonError(t, f.author(f.clone, input, "create-task", "--request-id", "canonical-refusal"), 4, "canonical-checkout")
	create := f.author(authoring, input, "create-task", "--request-id", "create-001")
	success(t, create)
	created := body(t, create)
	check(t, created, map[string]any{"data.outcome": "applied", "data.replayed": false})
	readyDigest := textField(t, created, "data.result.contentDigest")
	f.pendingReceipt(authoring, "create-001")
	recovered := f.author(authoring, input, "create-task", "--request-id", "create-001")
	success(t, recovered)
	equal(t, field(t, body(t, recovered), "data.outcome"), "applied")
	replay := f.author(authoring, input, "create-task", "--request-id", "create-001")
	success(t, replay)
	check(t, body(t, replay), map[string]any{"data.outcome": "already-applied", "data.replayed": true})
	changed := strings.Replace(input, "Coordinator task", "Different request", 1)
	jsonError(t, f.author(authoring, changed, "create-task", "--request-id", "create-001"), 5, "idempotency-conflict")
	blocked := encode(t, map[string]any{"expectedState": "ready", "expectedDigest": readyDigest, "state": "blocked", "blockedReason": "Waiting for approval.", "resolution": nil})
	transition := f.author(authoring, blocked, "transition-task", "coordinator-task", "--request-id", "transition-001")
	success(t, transition)
	blockedDigest := textField(t, body(t, transition), "data.result.contentDigest")
	pending := f.pendingReceipt(authoring, "transition-001")
	root := f.git(authoring, "rev-parse", "--show-toplevel")
	lockName := f.gitInput(authoring, filepath.Join(root, taskRoot, "coordinator-task.task.md"), "hash-object", "--stdin")
	lockRef := "refs/tsuzuri/locks/" + repositoryID + "/" + lockName
	f.git(authoring, "update-ref", lockRef, pending)
	interrupted := f.author(authoring, blocked, "transition-task", "coordinator-task", "--request-id", "transition-001")
	success(t, interrupted)
	equal(t, field(t, body(t, interrupted), "data.outcome"), "applied")
	equal(t, f.run(authoring, "", "git", "show-ref", "--verify", "--quiet", lockRef).code, 1)
	replay = f.author(authoring, blocked, "transition-task", "coordinator-task", "--request-id", "transition-001")
	success(t, replay)
	equal(t, field(t, body(t, replay), "data.outcome"), "already-applied")
	jsonError(t, f.author(authoring, blocked, "transition-task", "coordinator-task", "--request-id", "transition-stale"), 5, "stale-input")
	missing := encode(t, map[string]any{"expectedState": "blocked", "expectedDigest": blockedDigest, "state": "done", "blockedReason": nil, "resolution": nil})
	jsonError(t, f.author(authoring, missing, "transition-task", "coordinator-task", "--request-id", "transition-terminal"), 3, "validation")
	original := f.author(authoring, input, "create-task", "--request-id", "create-001")
	success(t, original)
	check(t, body(t, original), map[string]any{"data.outcome": "already-applied", "data.result.record.state": "ready"})
}
func TestCreatesProjectsWithImmutableReceipts(t *testing.T) {
	f := newFixture(t)
	dir := f.worktree("project-authoring")
	success(t, f.author(dir, `{"id":"integrations","title":"Integrations","state":"active","goal":"Coordinate external systems.","finishLine":"- Every operation is recoverable."}`, "create-project", "--request-id", "project-001"))
	check(t, f.ok(dir, "project", "integrations", "--source", "worktree"), map[string]any{"data.goal": "Coordinate external systems.", "data.finishLine": "- Every operation is recoverable."})
}
func TestFiltersEveryTaskKind(t *testing.T) {
	f := newFixture(t)
	for _, kind := range []string{"maintenance", "tech-debt", "operations", "research"} {
		f.task(kind+"-task", readyTask(kind+"-task", "p2", kind))
	}
	f.commitPush("add every task kind")
	for kind, id := range map[string]string{"delivery": "first-task", "defect": "second-task", "maintenance": "maintenance-task", "tech-debt": "tech-debt-task", "operations": "operations-task", "research": "research-task"} {
		v := f.ok(f.clone, "list", "--kind", kind)
		arrayLength(t, v, "data", 1)
		check(t, v, map[string]any{"data.0.id": id, "data.0.kind": kind})
	}
	next := f.ok(f.clone, "next", "--kind", "tech-debt")
	arrayLength(t, next, "data", 1)
	equal(t, field(t, next, "data.0.id"), "tech-debt-task")
	invalid := f.cli(f.clone, "--json", "list", "--kind", "not-a-kind")
	jsonError(t, invalid, 2, "usage")
	contains(t, invalid.stderr, "unknown task kind `not-a-kind`", true)
}
func TestRejectsTaskWithoutKind(t *testing.T) {
	f := newFixture(t)
	f.task("first-task", strings.ReplaceAll(readyTask("first-task", "p0", "delivery"), "kind = \"delivery\"\n", ""))
	r := f.cli(f.clone, "--json", "validate")
	jsonError(t, r, 3, "validation")
	contains(t, r.stderr, "kind is required", true)
}
func TestReportsIncompatibleRepositorySchema(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.clone, ".tsuzuri.toml")
	write(t, path, strings.Replace(read(t, path), "schema = 1", "schema = 2", 1))
	jsonError(t, f.cli(f.clone, "--json", "validate"), 3, "incompatible-schema")
}
func TestUnavailableTaskHasStableJSONError(t *testing.T) {
	f := newFixture(t)
	f.task("first-task", strings.ReplaceAll(readyTask("first-task", "p0", "delivery"), "state = \"ready\"", "state = \"backlog\""))
	f.commitPush("move task to backlog")
	jsonError(t, f.cli(f.clone, "--json", "claim", "first-task"), 4, "unavailable")
}
