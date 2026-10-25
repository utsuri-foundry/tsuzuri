//go:build linux || darwin

package tsuzuri

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRejectedPushDiagnostics(t *testing.T) {
	// Build before fixtures isolate HOME; no module downloads or credentials are
	// needed inside the disposable repositories. Exercise the actual CLI renderer.
	binary := filepath.Join(t.TempDir(), "tsuzuri")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, "./cmd/tsuzuri")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, b)
	}
	for _, scenario := range []string{"claim", "initial-assignment", "handoff", "accept", "release"} {
		t.Run(scenario, func(t *testing.T) {
			isolatedGit(t)
			f := fixture(t)
			client := clientAt(t, f.clone)
			cfg, err := parseConfig(testConfig)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"claim", "first-task", "--worker", "worker/a", "--no-worktree"}
			pattern := "refs/heads/tsuzuri/tasks*"
			if scenario == "initial-assignment" {
				pattern = "refs/heads/tsuzuri/tasks-assignments/*"
			}
			var claim ClaimResult
			if scenario == "handoff" || scenario == "accept" || scenario == "release" {
				claim = mustClaim(t, client, "worker/a")
			}
			switch scenario {
			case "handoff":
				args = []string{"handoff", "first-task", "--checkpoint", claim.Metadata.AnchorObjectID, "--to", "worker/b"}
			case "accept":
				_, err = client.Coordinate(ctx, Coordination{Command: "handoff", ID: "first-task", Checkpoint: claim.Metadata.AnchorObjectID})
				if err != nil {
					t.Fatal(err)
				}
				args = []string{"accept", "first-task", "--worker", "worker/b", "--no-worktree"}
			case "release":
				args = []string{"release", "first-task"}
			}
			remoteRefs := func() string {
				return checkedGit(t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/tsuzuri/")
			}
			beforeRemote := remoteRefs()
			beforeReceipt := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname) %(objectname)", cfg.receipt("first-task"), cfg.assignmentReceipt("first-task"))
			hook := filepath.Join(f.remote, "hooks", "pre-receive")
			body := "#!/bin/sh\nwhile read old new ref; do\ncase \"$ref\" in " + pattern + ")\nprintf '%s\\n' 'policy: test writes prohibited' 'https://operator:hook-secret@example.invalid/repo' 'Authorization: Bearer hook-token' >&2\nexit 1\n;; esac\ndone\n"
			if err := os.WriteFile(hook, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, binary, append([]string{"--json"}, args...)...)
			command.Dir = f.clone
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err = command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 5 {
				t.Fatalf("expected exit 5: %v\n%s\n%s", err, &stdout, &stderr)
			}
			var envelope struct {
				Schema int
				OK     bool
				Error  *Error
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Schema != 1 || envelope.OK || envelope.Error == nil || envelope.Error.Kind != "claim-conflict" {
				t.Fatalf("wrong envelope: %s", &stdout)
			}
			for _, text := range []string{envelope.Error.Message, stderr.String()} {
				if !strings.Contains(text, "policy: test writes prohibited") {
					t.Fatalf("lost server diagnostic: %s", text)
				}
				for _, forbidden := range []string{"hook-secret", "hook-token", "receipt retained", "ownership changed"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("unsafe or inaccurate diagnostic: %s", text)
					}
				}
			}
			if scenario == "claim" || scenario == "initial-assignment" {
				receipt := checkedGit(t, f.clone, "for-each-ref", "--format=%(objectname)", cfg.receipt("first-task"))
				if receipt == "" {
					t.Fatal("lost claim receipt")
				}
				if scenario == "initial-assignment" && checkedGit(t, f.remote, "for-each-ref", "--format=%(objectname)", cfg.claimRef("first-task")) != receipt {
					t.Fatal("initial assignment failure did not retain the acquired claim")
				}
				if checkedGit(t, f.remote, "for-each-ref", "--format=%(objectname)", cfg.assignmentRef("first-task")) != "" {
					t.Fatal("rejected assignment was published")
				}
				if scenario == "claim" && remoteRefs() != beforeRemote {
					t.Fatal("rejected claim changed remote refs")
				}
			} else {
				if remoteRefs() != beforeRemote {
					t.Fatal("rejected push changed remote refs")
				}
				after := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname) %(objectname)", cfg.receipt("first-task"), cfg.assignmentReceipt("first-task"))
				if after != beforeReceipt {
					t.Fatal("rejected push changed receipts")
				}
			}
		})
	}
}

func TestPushExecFailureCauses(t *testing.T) {
	readErr := errors.New("exact ref read failed")
	err := pushFailure(failure("indeterminate", "could not reconcile remote write").causedBy(readErr), gitOutput{diagnostic: "Authorization: secret-header"}, context.DeadlineExceeded)
	if !errors.Is(err, readErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost causes: %v", err)
	}
	if !strings.Contains(err.Message, context.DeadlineExceeded.Error()) || strings.Contains(err.Message, "secret-header") {
		t.Fatalf("unsafe or missing exec diagnostic: %v", err)
	}
	fallback := pushFailure(failure("claim-conflict", "conditional remote write was not acquired"), gitOutput{code: 128}, nil)
	if !strings.Contains(fallback.Message, "Git push exited 128") {
		t.Fatalf("missing exit code: %v", fallback)
	}
}
