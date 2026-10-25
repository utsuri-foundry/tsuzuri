package tsuzuri

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func orphanFixture(t *testing.T) (testRepository, *Client, Config, ClaimResult) {
	t.Helper()
	f := fixture(t)
	c := clientAt(t, f.clone)
	claim := mustClaim(t, c, "worker/a")
	cfg, err := parseConfig(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.remote, "update-ref", "-d", cfg.claimRef("first-task"), claim.Metadata.AnchorObjectID)
	return f, c, cfg, claim
}
func coordinationRefs(t *testing.T, dir string, cfg Config) string {
	t.Helper()
	return checkedGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", cfg.claimRef("first-task"), cfg.assignmentRef("first-task"), cfg.receipt("first-task"), cfg.assignmentReceipt("first-task"))
}
func requireOrphanError(t *testing.T, err error, cfg Config, assignment string) {
	t.Helper()
	var app *Error
	if !errors.As(err, &app) || app.Kind != "claim-conflict" || app.ExitCode != 5 {
		t.Fatalf("expected orphan conflict: %v", err)
	}
	body, err := json.Marshal(app.Details)
	if err != nil {
		t.Fatal(err)
	}
	var d OrphanAssignmentDetails
	if err = json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Issue != "orphan-assignment" || d.RepositoryID != cfg.RepositoryID || d.TaskID != "first-task" || d.ClaimRef != cfg.claimRef("first-task") || d.ClaimObjectID != nil || d.AssignmentRef != cfg.assignmentRef("first-task") || d.AssignmentObjectID != assignment || d.SnapshotID == "" || !validObject(d.BaseObjectID) || !strings.Contains(d.SafeAction, "do not delete") {
		t.Fatalf("missing orphan evidence/action: %s", body)
	}
}
func TestOrphanFreshClonePreservesEvidence(t *testing.T) {
	isolatedGit(t)
	for _, command := range []string{"claim", "accept", "handoff", "materialize", "release"} {
		t.Run(command, func(t *testing.T) {
			f, _, cfg, claim := orphanFixture(t)
			clone := cloneFixture(t, f, "fresh")
			remoteBefore := coordinationRefs(t, f.remote, cfg)
			localBefore := coordinationRefs(t, clone, cfg)
			_, err := clientAt(t, clone).Coordinate(context.Background(), Coordination{Command: command, ID: "first-task", Worker: "worker/a", NoWorktree: true})
			requireOrphanError(t, err, cfg, claim.Assignment.ObjectID)
			if coordinationRefs(t, f.remote, cfg) != remoteBefore || coordinationRefs(t, clone, cfg) != localBefore {
				t.Fatal("orphan refusal mutated coordination state")
			}
			if _, err := os.Stat(filepath.Join(f.root, "wt-first-task")); !os.IsNotExist(err) {
				t.Fatal("orphan refusal materialized a worktree")
			}
			status, err := clientAt(t, clone).Query(context.Background(), Query{Command: "status", ID: "first-task"})
			if err != nil {
				t.Fatal(err)
			}
			if len(status.Data.(map[string]any)["orphanAssignments"].([]AssignmentObservation)) != 1 {
				t.Fatal("status lost orphan evidence")
			}
		})
	}
}
func TestOrphanReceiptBackedClaimRecovery(t *testing.T) {
	isolatedGit(t)
	for _, mode := range []string{"matching", "lost-assignment-receipt", "wrong-worker", "wrong-owner", "wrong-digest", "lost-both-receipts", "advanced-local-branch", "dirty-worktree"} {
		t.Run(mode, func(t *testing.T) {
			f, c, cfg, claim := orphanFixture(t)
			q := Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true, ExpectedDigest: claim.ClaimedTask.RecordDigest}
			switch mode {
			case "wrong-worker":
				q.Worker = "worker/b"
			case "wrong-owner":
				q.Owner = "other-owner"
			case "wrong-digest":
				q.ExpectedDigest = strings.Repeat("a", 40)
			case "lost-assignment-receipt":
				checkedGit(t, f.clone, "update-ref", "-d", cfg.assignmentReceipt(q.ID))
			case "lost-both-receipts":
				checkedGit(t, f.clone, "update-ref", "-d", cfg.assignmentReceipt(q.ID))
				checkedGit(t, f.clone, "update-ref", "-d", cfg.receipt(q.ID))
			case "advanced-local-branch":
				checkedGit(t, f.clone, "checkout", "-b", "tsuzuri/tasks/first-task", claim.Metadata.AnchorObjectID)
				historyWrite(t, f, "implementation", "useful work")
			case "dirty-worktree":
				path := filepath.Join(f.root, "wt-first-task")
				checkedGit(t, f.clone, "worktree", "add", "-b", "tsuzuri/tasks/first-task", path, claim.Metadata.AnchorObjectID)
				writeTestFile(t, filepath.Join(path, "implementation"), "uncommitted work")
			}
			before := coordinationRefs(t, f.clone, cfg)
			remoteBefore := coordinationRefs(t, f.remote, cfg)
			result, err := c.Coordinate(context.Background(), q)
			if mode == "matching" || mode == "lost-assignment-receipt" {
				if err != nil {
					t.Fatal(err)
				}
				recovered := result.Data.(ClaimResult)
				if recovered.Metadata != claim.Metadata || recovered.ClaimedTask != claim.ClaimedTask || recovered.Assignment.ObjectID != claim.Assignment.ObjectID {
					t.Fatal("recovery replaced original claim/assignment/revision")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe orphan recovery succeeded")
				}
				if coordinationRefs(t, f.clone, cfg) != before || coordinationRefs(t, f.remote, cfg) != remoteBefore {
					t.Fatal("refused recovery changed evidence")
				}
			}
		})
	}
}
func TestRustOrphanFreshClaimDifference(t *testing.T) {
	binary := rustBinary(t)
	isolatedGit(t)
	// Rust treats an explicitly empty launcher worker as invalid input. This
	// reproduction deliberately requests a claim without a worker assignment.
	if err := os.Unsetenv("TSUZURI_WORKER"); err != nil {
		t.Fatal(err)
	}
	f, _, cfg, claim := orphanFixture(t)
	goClone := cloneFixture(t, f, "go-fresh")
	_, err := clientAt(t, goClone).Coordinate(context.Background(), Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true})
	requireOrphanError(t, err, cfg, claim.Assignment.ObjectID)
	rustClone := cloneFixture(t, f, "rust-fresh")
	if response, err := rustCommand(binary, rustClone, "claim", "first-task", "--no-worktree"); err != nil {
		t.Fatalf("Rust reference result changed: %v %+v", err, response)
	}
	if checkedGit(t, f.remote, "rev-parse", cfg.assignmentRef("first-task")) != claim.Assignment.ObjectID {
		t.Fatal("Rust changed surviving assignment")
	}
	status, err := clientAt(t, goClone).Query(context.Background(), Query{Command: "status", ID: "first-task"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Data.(map[string]any)["invalidAssignments"].(int) != 1 {
		t.Fatal("new Rust claim did not reproduce invalid surviving assignment")
	}
}

func TestOrphanPartialReleaseRecovery(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	c := clientAt(t, f.clone)
	claim := mustClaim(t, c, "worker/a")
	cfg, err := parseConfig(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	receipts := coordinationRefs(t, f.clone, cfg)
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	script := "#!/bin/sh\nwhile read old new ref; do\ncase \"$ref\" in refs/heads/tsuzuri/tasks-assignments/*) echo 'policy: preserve assignment' >&2; exit 1;; esac\ndone\n"
	if err = os.WriteFile(hook, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = c.Coordinate(context.Background(), Coordination{Command: "release", ID: "first-task"})
	expectKind(t, err, "claim-conflict")
	if checkedGit(t, f.remote, "for-each-ref", "--format=%(objectname)", cfg.claimRef("first-task")) != "" {
		t.Fatal("claim deletion did not produce partial release")
	}
	if checkedGit(t, f.remote, "rev-parse", cfg.assignmentRef("first-task")) != claim.Assignment.ObjectID || coordinationRefs(t, f.clone, cfg) != receipts {
		t.Fatal("partial release lost receipts or assignment")
	}
	fresh := clientAt(t, cloneFixture(t, f, "fresh"))
	_, err = fresh.Coordinate(context.Background(), Coordination{Command: "claim", ID: "first-task", Worker: "worker/b", NoWorktree: true})
	requireOrphanError(t, err, cfg, claim.Assignment.ObjectID)
	if err = os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	result, err := c.Coordinate(context.Background(), Coordination{Command: "release", ID: "first-task"})
	if err != nil {
		t.Fatal(err)
	}
	released := result.Data.(ReleaseResult)
	if !released.RemoteAlreadyAbsent || !released.RemovedRemoteAssignment || !released.RemovedReceipt || !released.RemovedAssignmentReceipt {
		t.Fatalf("incomplete release: %+v", released)
	}
	if coordinationRefs(t, f.remote, cfg) != "" || coordinationRefs(t, f.clone, cfg) != "" {
		t.Fatal("unused cleanup incomplete")
	}
	// A later fresh claim uses a new identity only after both old refs are gone.
	next := mustClaim(t, fresh, "worker/b")
	if next.Metadata.ClaimID == claim.Metadata.ClaimID {
		t.Fatal("new claim reused old identity")
	}
}

func TestOrphanChangedOrPendingAssignment(t *testing.T) {
	isolatedGit(t)
	for _, to := range []string{"worker/b", ""} {
		t.Run("to-"+to, func(t *testing.T) {
			f := fixture(t)
			a := clientAt(t, f.clone)
			claim := mustClaim(t, a, "worker/a")
			cfg, err := parseConfig(testConfig)
			if err != nil {
				t.Fatal(err)
			}
			b := clientAt(t, cloneFixture(t, f, "handoff"))
			if _, err = b.Coordinate(context.Background(), Coordination{Command: "accept", ID: "first-task", Worker: "worker/a", NoWorktree: true}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Coordinate(context.Background(), Coordination{Command: "handoff", ID: "first-task", To: to, Checkpoint: claim.Metadata.AnchorObjectID}); err != nil {
				t.Fatal(err)
			}
			checkedGit(t, f.remote, "update-ref", "-d", cfg.claimRef("first-task"), claim.Metadata.AnchorObjectID)
			beforeRemote, beforeLocal := coordinationRefs(t, f.remote, cfg), coordinationRefs(t, f.clone, cfg)
			for _, command := range []string{"claim", "release", "materialize", "accept"} {
				_, err = a.Coordinate(context.Background(), Coordination{Command: command, ID: "first-task", Worker: "worker/a", NoWorktree: true})
				if err == nil {
					t.Fatalf("outgoing worker recovered via %s", command)
				}
				if coordinationRefs(t, f.remote, cfg) != beforeRemote || coordinationRefs(t, f.clone, cfg) != beforeLocal {
					t.Fatal("outgoing worker changed orphan evidence")
				}
			}
		})
	}
}

func TestOrphanAssignmentChangesAfterSnapshot(t *testing.T) {
	isolatedGit(t)
	for _, command := range []string{"claim", "release", "release-active"} {
		t.Run(command, func(t *testing.T) {
			f, c, cfg, claim := orphanFixture(t)
			if command == "release-active" {
				checkedGit(t, f.remote, "update-ref", cfg.claimRef("first-task"), claim.Metadata.AnchorObjectID)
			}
			ctx := context.Background()
			r, err := c.session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s, err := r.snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			worker := "worker/b"
			next, err := r.newAssignment(ctx, claim.Metadata, claim.Assignment.ObjectID, &worker)
			if err != nil {
				t.Fatal(err)
			}
			checkedGit(t, f.clone, "push", "origin", next.ObjectID+":"+cfg.assignmentRef("first-task"))
			beforeRemote, beforeLocal := coordinationRefs(t, f.remote, cfg), coordinationRefs(t, f.clone, cfg)
			if command == "claim" {
				_, err = r.claim(ctx, s, Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true})
			} else {
				_, err = r.release(ctx, s, "first-task")
			}
			expectKind(t, err, "lost-ownership")
			if coordinationRefs(t, f.remote, cfg) != beforeRemote || coordinationRefs(t, f.clone, cfg) != beforeLocal {
				t.Fatal("stale recovery changed evidence")
			}
		})
	}
}

func TestOrphanKnownImplementationCannotBeForgotten(t *testing.T) {
	isolatedGit(t)
	for _, command := range []string{"claim", "release"} {
		t.Run(command, func(t *testing.T) {
			f := fixture(t)
			c := clientAt(t, f.clone)
			claim := mustClaim(t, c, "worker/a")
			cfg, err := parseConfig(testConfig)
			if err != nil {
				t.Fatal(err)
			}
			publisher := cloneFixture(t, f, "publisher")
			checkedGit(t, publisher, "checkout", "-b", "work", claim.Metadata.AnchorObjectID)
			writeTestFile(t, filepath.Join(publisher, "implementation"), "preserved work")
			checkedGit(t, publisher, "add", "implementation")
			checkedGit(t, publisher, "commit", "-m", "implementation")
			tip := checkedGit(t, publisher, "rev-parse", "HEAD")
			checkedGit(t, publisher, "push", "origin", "HEAD:"+cfg.claimRef("first-task"))
			ctx := context.Background()
			r, err := c.session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s, err := r.snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			checkedGit(t, f.remote, "update-ref", "-d", cfg.claimRef("first-task"), tip)
			beforeRemote, beforeLocal := coordinationRefs(t, f.remote, cfg), coordinationRefs(t, f.clone, cfg)
			if command == "claim" {
				_, err = r.claim(ctx, s, Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true})
			} else {
				_, err = r.release(ctx, s, "first-task")
			}
			expectKind(t, err, "lost-ownership")
			if coordinationRefs(t, f.remote, cfg) != beforeRemote || coordinationRefs(t, f.clone, cfg) != beforeLocal {
				t.Fatal("forgot known implementation after remote disappearance")
			}
			if checkedGit(t, publisher, "rev-parse", "HEAD") != tip {
				t.Fatal("publisher work changed")
			}
		})
	}
}

func TestOrphanReleasePreservesRecreatedClaim(t *testing.T) {
	isolatedGit(t)
	for _, when := range []string{"claim-deletion", "assignment-deletion"} {
		t.Run(when, func(t *testing.T) {
			f := fixture(t)
			c := clientAt(t, f.clone)
			claim := mustClaim(t, c, "worker/a")
			cfg, err := parseConfig(testConfig)
			if err != nil {
				t.Fatal(err)
			}
			target := cfg.claimRef("first-task")
			if when == "assignment-deletion" {
				target = cfg.assignmentRef("first-task")
			}
			hook := "#!/bin/sh\nwhile read old new ref; do\nif test \"$ref\" = '" + target + "' && test \"$new\" = '" + strings.Repeat("0", len(claim.Metadata.AnchorObjectID)) + "'; then git update-ref '" + cfg.claimRef("first-task") + "' '" + claim.Metadata.AnchorObjectID + "'; fi\ndone\n"
			if err = os.WriteFile(filepath.Join(f.remote, "hooks", "post-receive"), []byte(hook), 0700); err != nil {
				t.Fatal(err)
			}
			before := coordinationRefs(t, f.clone, cfg)
			_, err = c.Coordinate(context.Background(), Coordination{Command: "release", ID: "first-task"})
			want := "preserved-work"
			if when == "claim-deletion" {
				want = "claim-conflict" // The deletion's own reconciliation sees it.
			}
			expectKind(t, err, want)
			if coordinationRefs(t, f.clone, cfg) != before {
				t.Fatal("recreated claim caused receipt cleanup")
			}
			if checkedGit(t, f.remote, "rev-parse", cfg.claimRef("first-task")) != claim.Metadata.AnchorObjectID {
				t.Fatal("recreated claim was deleted")
			}
			if when == "claim-deletion" && checkedGit(t, f.remote, "rev-parse", cfg.assignmentRef("first-task")) != claim.Assignment.ObjectID {
				t.Fatal("assignment deleted despite observed recreated claim")
			}
		})
	}
}

func TestOrphanReleaseRechecksClaimBeforeAssignmentDeletion(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	c := clientAt(t, f.clone)
	claim := mustClaim(t, c, "worker/a")
	cfg, err := parseConfig(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	policy := operationPolicy(t, f)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "git")
	script := `#!/bin/sh
case "$*" in
 *'ls-remote --refs origin refs/heads/tsuzuri/tasks/first-task')
  n=0
  if test -f "$COUNTER"; then read n < "$COUNTER"; fi
  n=$((n+1))
  printf '%s\n' "$n" > "$COUNTER"
  if test "$n" = 4; then
   "$REAL_GIT" --git-dir="$REMOTE" update-ref refs/heads/tsuzuri/tasks/first-task "$ANCHOR" || exit
  fi
 ;;
esac
exec "$REAL_GIT" "$@"
`
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	policy.Environment = append(policy.Environment, "REAL_GIT="+policy.GitExecutable, "COUNTER="+filepath.Join(dir, "count"), "REMOTE="+f.remote, "ANCHOR="+claim.Metadata.AnchorObjectID)
	policy.GitExecutable = wrapper
	before := coordinationRefs(t, f.clone, cfg)
	_, err = c.Coordinate(context.Background(), Coordination{Command: "release", ID: "first-task"}, policy)
	expectKind(t, err, "preserved-work")
	if coordinationRefs(t, f.clone, cfg) != before || checkedGit(t, f.remote, "rev-parse", cfg.assignmentRef("first-task")) != claim.Assignment.ObjectID {
		t.Fatal("cleanup ignored recreated claim before assignment deletion")
	}
}

func TestOrphanAppearingAfterFreshSnapshot(t *testing.T) {
	isolatedGit(t)
	f, _, cfg, claim := orphanFixture(t)
	checkedGit(t, f.remote, "update-ref", "-d", cfg.assignmentRef("first-task"), claim.Assignment.ObjectID)
	fresh := cloneFixture(t, f, "fresh")
	ctx := context.Background()
	r, err := clientAt(t, fresh).session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.remote, "update-ref", cfg.assignmentRef("first-task"), claim.Assignment.ObjectID)
	beforeRemote, beforeLocal := coordinationRefs(t, f.remote, cfg), coordinationRefs(t, fresh, cfg)
	_, err = r.claim(ctx, s, Coordination{Command: "claim", ID: "first-task", Worker: "worker/a", NoWorktree: true})
	expectKind(t, err, "lost-ownership")
	if coordinationRefs(t, f.remote, cfg) != beforeRemote || coordinationRefs(t, fresh, cfg) != beforeLocal {
		t.Fatal("fresh claim ignored assignment appearing after snapshot")
	}
}
