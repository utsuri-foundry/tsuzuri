package tsuzuri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorrectedTerminalHistoryRecovery(t *testing.T) {
	isolatedGit(t)
	for _, state := range []string{"done", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			client := clientAt(t, f.clone)
			claim := mustClaim(t, client, "original-worker")
			other := "tasks/projects/example/tasks/other-task.task.md"
			terminalDoc := strings.ReplaceAll(strings.ReplaceAll(historyTerminal(), "first-task", "other-task"), `state = "done"`, fmt.Sprintf("state = %q", state))
			historyWrite(t, f, other, strings.ReplaceAll(terminalDoc, "# Resolution", "# Delivery"))
			bad := checkedGit(t, f.clone, "rev-parse", "HEAD")
			checkedGit(t, f.clone, "push", "origin", "main")
			_, _, err := client.Show(ctx, "first-task")
			assertRecordError(t, err, bad, other, "Resolution")
			historyWrite(t, f, other, terminalDoc)
			historyWrite(t, f, "tasks/projects/example/tasks/next-task.task.md", strings.ReplaceAll(testTask, "first-task", "next-task"))
			checkedGit(t, f.clone, "push", "origin", "main")
			checkedGit(t, f.clone, "merge-base", "--is-ancestor", bad, "HEAD")
			remoteBefore := checkedGit(t, f.remote, "show-ref")
			receiptsBefore := checkedGit(t, f.clone, "for-each-ref", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts")
			for _, q := range []Query{{Command: "validate"}, {Command: "validate", Source: "base"}, {Command: "next"}, {Command: "status"}, {Command: "overview"}} {
				if _, err := client.Query(ctx, q); err != nil {
					t.Fatalf("%+v after correction: %v", q, err)
				}
			}
			view, _, err := client.Show(ctx, "first-task")
			if err != nil || view.Claim == nil || !view.Claim.Valid || view.ClaimedMarkdown == nil || *view.ClaimedMarkdown != testTask || view.Claim.ClaimedTask.RecordDigest != claim.ClaimedTask.RecordDigest {
				t.Fatalf("original authority changed: %+v, %v", view.Claim, err)
			}
			finished, _, err := client.Show(ctx, "other-task")
			if err != nil || finished.State != state || finished.CanonicallyClaimable {
				t.Fatalf("terminal task became claimable: %+v, %v", finished.TaskView, err)
			}
			if remoteBefore != checkedGit(t, f.remote, "show-ref") || receiptsBefore != checkedGit(t, f.clone, "for-each-ref", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts") {
				t.Fatal("inspection changed coordination refs or receipts")
			}
			// A fresh clone has neither response nor local receipts. Recovery must
			// retain the old anchor and assignment, and reject every other worker.
			recoverer := clientAt(t, cloneFixture(t, f, "recoverer"))
			_, _, err = recoverer.Accept(ctx, AcceptRequest{Task: "first-task", Worker: "wrong-worker", NoWorktree: true})
			expectKind(t, err, "claim-conflict")
			accepted, _, err := recoverer.Accept(ctx, AcceptRequest{Task: "first-task", Worker: "original-worker", NoWorktree: true})
			if err != nil || accepted.ClaimObjectID != claim.Metadata.AnchorObjectID || accepted.ClaimedTask == nil || accepted.ClaimedTask.RecordDigest != claim.ClaimedTask.RecordDigest {
				t.Fatalf("exact-worker recovery: %+v, %v", accepted, err)
			}
			if remoteBefore != checkedGit(t, f.remote, "show-ref") {
				t.Fatal("recovery replaced original remote claim or assignment")
			}
			next, _, err := recoverer.Show(ctx, "next-task")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = recoverer.Claim(ctx, ClaimRequest{Task: "next-task", Worker: "next-worker", NoWorktree: true, ExpectedDigest: next.RecordDigest}); err != nil {
				t.Fatalf("unrelated new claim blocked: %v", err)
			}
			for _, name := range []string{"TSUZURI_HISTORY_BASELINE", "TSUZURI_RUST_BINARY"} {
				if binary := os.Getenv(name); binary != "" {
					command := exec.CommandContext(ctx, binary, "status", "--json")
					command.Dir = cloneFixture(t, f, strings.ToLower(name))
					output, err := command.CombinedOutput()
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(string(output), "Resolution") {
						t.Fatalf("expected documented old-client refusal from %s: %v\n%s", name, err, output)
					}
				}
			}
		})
	}
}

func TestHistoricalTerminalSectionsOnly(t *testing.T) {
	isolatedGit(t)
	for _, state := range []string{"done", "cancelled"} {
		for _, body := range []string{"", "# Delivery\n\nFinished.\n", "# Resolution\n", "# Resolution\n\nOne.\n\n# Resolution\n\nTwo.\n", "## Resolution\n\nFinished.\n"} {
			t.Run(fmt.Sprintf("%s/%q", state, body), func(t *testing.T) {
				f := fixture(t)
				front, _, err := document(strings.ReplaceAll(historyTerminal(), `state = "done"`, fmt.Sprintf("state = %q", state)))
				if err != nil {
					t.Fatal(err)
				}
				malformed := "+++\n" + front + "\n+++\n" + body
				historyWrite(t, f, historyTaskPath, malformed)
				bad := checkedGit(t, f.clone, "rev-parse", "HEAD")
				historyWrite(t, f, historyTaskPath, strings.ReplaceAll(historyTerminal(), `state = "done"`, fmt.Sprintf("state = %q", state)))
				r, _ := cachedRepository(t, f)
				ctx := context.Background()
				if _, err := r.tree(ctx, "HEAD", false, true); err != nil {
					t.Fatal(err)
				}
				// Completed history proof must never turn a historical parse into
				// a strictly valid inventory, including the claimed-snapshot path.
				for _, legacy := range []bool{false, true} {
					_, err := r.tree(ctx, bad, legacy, true)
					assertRecordError(t, err, bad, historyTaskPath, "Resolution")
				}
			})
		}
	}
}

func TestCorrectedHistoryDoesNotRepairClaimedSnapshot(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	other := "tasks/projects/example/tasks/other-task.task.md"
	terminalDoc := strings.ReplaceAll(historyTerminal(), "first-task", "other-task")
	historyWrite(t, f, other, strings.ReplaceAll(terminalDoc, "# Resolution", "# Delivery"))
	bad := checkedGit(t, f.clone, "rev-parse", "HEAD")
	historyWrite(t, f, other, terminalDoc)
	checkedGit(t, f.clone, "push", "origin", "main")
	ctx := context.Background()
	client := clientAt(t, f.clone)
	r, err := client.session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Construct evidence that a client claimed against the malformed tree.
	// The corrected current snapshot cannot authorize that original revision.
	s.base = bad
	claim, err := r.newClaim(ctx, s, "first-task", "test-owner")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := r.newAssignment(ctx, claim, claim.AnchorObjectID, stringPtr("worker"))
	if err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.clone, "push", "origin", claim.AnchorObjectID+":"+s.loaded.Config.claimRef("first-task"), assignment.ObjectID+":"+s.loaded.Config.assignmentRef("first-task"))
	before := checkedGit(t, f.remote, "show-ref")
	view, _, err := client.Show(ctx, "first-task")
	if err != nil || view.Claim == nil || view.Claim.Valid || view.Claim.Error == nil || !strings.Contains(*view.Claim.Error, bad) || !strings.Contains(*view.Claim.Error, "Resolution") {
		t.Fatalf("malformed original snapshot was authorized: %+v, %v", view.Claim, err)
	}
	if _, _, err = client.Accept(ctx, AcceptRequest{Task: "first-task", Worker: "worker", NoWorktree: true}); err == nil {
		t.Fatal("accepted claim against malformed original snapshot")
	}
	if before != checkedGit(t, f.remote, "show-ref") || checkedGit(t, f.clone, "for-each-ref", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts") != "" {
		t.Fatal("failed acceptance changed coordination evidence")
	}
}

func TestCorrectedTerminalHistoryRetainsRejections(t *testing.T) {
	isolatedGit(t)
	badTerminal := strings.ReplaceAll(historyTerminal(), "# Resolution", "# Delivery")
	for _, tc := range []struct {
		name, path, want string
		edit             func(*testing.T, testRepository)
	}{
		{"kind", historyTaskPath, "kind", func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(badTerminal, `kind = "delivery"`, `kind = "unknown"`))
		}},
		{"schema", historyTaskPath, "schema", func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(badTerminal, "schema = 1", "schema = 2"))
		}},
		{"date", historyTaskPath, "date", func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(badTerminal, "created = 2026-10-24", "created = 42"))
		}},
		{"framing", historyTaskPath, "LF", func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(badTerminal, "\n", "\r\n"))
		}},
		{"reopen", historyTaskPath, "terminal task", func(t *testing.T, f testRepository) { historyWrite(t, f, historyTaskPath, testTask) }},
		{"remove", historyTaskPath, "terminal task", func(t *testing.T, f testRepository) { historyRemove(t, f, historyTaskPath) }},
		{"change-terminal-state", historyTaskPath, "terminal task", func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(badTerminal, `state = "done"`, `state = "cancelled"`))
		}},
		{"move", historyTaskPath, "terminal task", func(t *testing.T, f testRepository) { historyMove(t, f, true, false) }},
		{"root-switch", historyTaskPath, "terminal task", func(t *testing.T, f testRepository) { historyRootSwitch(t, f, true) }},
		{"repository-id", ".tsuzuri.toml", "repository_id", func(t *testing.T, f testRepository) {
			historyWrite(t, f, ".tsuzuri.toml", strings.ReplaceAll(testConfig, "0198d2de", "1198d2de"))
		}},
		{"configuration", ".tsuzuri.toml", "TOML", func(t *testing.T, f testRepository) { historyWrite(t, f, ".tsuzuri.toml", "broken") }},
		{"nonterminal-acceptance", "tasks/projects/example/tasks/other-task.task.md", "Acceptance", func(t *testing.T, f testRepository) {
			historyWrite(t, f, "tasks/projects/example/tasks/other-task.task.md", strings.ReplaceAll(strings.ReplaceAll(testTask, "first-task", "other-task"), "# Acceptance", "# Checks"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			historyWrite(t, f, historyTaskPath, badTerminal)
			before := checkedGit(t, f.clone, "rev-parse", "HEAD")
			tc.edit(t, f)
			bad := checkedGit(t, f.clone, "rev-parse", "HEAD")
			checkedGit(t, f.clone, "restore", "--source="+before, "--staged", "--worktree", ".")
			writeTestFile(t, filepath.Join(f.clone, historyTaskPath), historyTerminal())
			historyCommit(t, f)
			r, _ := cachedRepository(t, f)
			_, err := r.tree(context.Background(), "HEAD", false, true)
			assertRecordError(t, err, bad, tc.path, tc.want)
			if len(r.histories) != 0 || len(r.cache.firstParents) != 0 {
				t.Fatal("failed replay supplied a validation or ancestry proof")
			}
		})
	}
}

func assertRecordError(t *testing.T, err error, commit, path, text string) {
	t.Helper()
	var app *Error
	if !errors.As(err, &app) || app.ExitCode != 3 || !strings.Contains(app.Message, text) || !strings.Contains(app.Message, commit) || !strings.Contains(app.Message, fmt.Sprintf("%q", path)) || app.Unwrap() == nil {
		t.Fatalf("missing classified record diagnostic: %v", err)
	}
	data, marshalErr := json.Marshal(app.Details)
	var location recordLocation
	if marshalErr != nil || json.Unmarshal(data, &location) != nil || location.Commit != commit || location.Path != path {
		t.Fatalf("missing structured location: %s, %v", data, marshalErr)
	}
	var cause *Error
	if !errors.As(app.Unwrap(), &cause) || app.Kind != cause.Kind || app.Code != cause.Code || app.ExitCode != cause.ExitCode {
		t.Fatalf("classification changed: %+v", app)
	}
}

func TestRecordErrorQuotesPathsAndPreservesCause(t *testing.T) {
	cause := failure("incompatible-schema", "unsupported schema")
	err := recordError(strings.Repeat("a", 40), "odd\n\tpath.task.md", cause)
	assertRecordError(t, err, strings.Repeat("a", 40), "odd\n\tpath.task.md", "unsupported schema")
	if !errors.Is(err, cause) || strings.ContainsAny(err.Message, "\n\t") {
		t.Fatalf("cause lost or control characters exposed: %q", err.Message)
	}
}
