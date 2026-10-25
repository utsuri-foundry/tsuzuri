package tsuzuri

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func operationPolicy(t *testing.T, f testRepository) OperationOptions {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return OperationOptions{
		GitExecutable:          git,
		Environment:            []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=file"},
		ExpectedRemoteURL:      f.remote,
		ExpectedRepositoryID:   "0198d2de-18c7-7ca0-a327-e6b4760f8613",
		ExpectedClaimNamespace: "tsuzuri/tasks",
	}
}

func TestOperationEnvironmentIsolation(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	client := clientAt(t, f.clone)
	policies := []OperationOptions{operationPolicy(t, f), operationPolicy(t, f)}
	for i := range policies {
		policies[i].Environment = append(policies[i].Environment, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://example.invalid/.extraheader", fmt.Sprintf("GIT_CONFIG_VALUE_0=Authorization: dummy-secret-%d", i))
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	for i, policy := range policies {
		go func() {
			<-start
			ctx := context.Background()
			r, err := client.session(ctx, policy)
			if err != nil {
				done <- err
				return
			}
			for range 3 {
				got, err := r.run(ctx, "config", "--get", "http.https://example.invalid/.extraheader")
				if err != nil {
					done <- err
					return
				}
				if strings.TrimSpace(got) != fmt.Sprintf("Authorization: dummy-secret-%d", i) {
					done <- errors.New("operation credential crossed callers")
					return
				}
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err = r.run(cancelled, "status"); !errors.Is(err, context.Canceled) {
				done <- fmt.Errorf("lost cancellation: %w", err)
				return
			}
			// A cancelled operation must not poison a later independent call.
			_, _, err = client.Show(ctx, "first-task", policy)
			done <- err
		}()
	}
	close(start)
	for range policies {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("GIT_CONFIG_VALUE_0") == "Authorization: dummy-secret-0" || os.Getenv("GIT_CONFIG_VALUE_0") == "Authorization: dummy-secret-1" {
		t.Fatal("mutated process environment")
	}
}

func TestOperationPolicyFailsBeforeClaimEffects(t *testing.T) {
	isolatedGit(t)
	for _, kind := range []string{"remote", "rewritten-remote", "repository", "namespace", "remote-repository", "remote-namespace"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t)
			policy := operationPolicy(t, f)
			switch kind {
			case "remote":
				policy.ExpectedRemoteURL = filepath.Join(f.root, "different.git")
			case "rewritten-remote":
				checkedGit(t, f.clone, "config", "url."+filepath.Join(f.root, "different.git")+".insteadOf", f.remote)
			case "repository":
				policy.ExpectedRepositoryID = "different-repository"
			case "namespace":
				policy.ExpectedClaimNamespace = "different/claims"
			case "remote-repository":
				writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), strings.ReplaceAll(testConfig, policy.ExpectedRepositoryID, "0198d2de-18c7-7ca0-a327-e6b4760f8614"))
				checkedGit(t, f.clone, "commit", "-am", "change canonical repository ID")
				checkedGit(t, f.clone, "push", "origin", "main")
				writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), testConfig)
			case "remote-namespace":
				writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), strings.ReplaceAll(testConfig, "tsuzuri/tasks", "different/tasks"))
				checkedGit(t, f.clone, "commit", "-am", "change canonical namespace")
				checkedGit(t, f.clone, "push", "origin", "main")
				writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), testConfig)
			}
			_, _, err := clientAt(t, f.clone).Claim(context.Background(), ClaimRequest{Task: "first-task", Worker: "worker-a", NoWorktree: true}, policy)
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != "validation" || failure.Code != "validation" || failure.ExitCode != 3 {
				t.Fatalf("wrong policy rejection: %v", err)
			}
			// Read the bare fixture directly so URL rewriting cannot hide effects.
			if got := checkedGit(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/tsuzuri/", "refs/heads/tsuzuri-assignments/", "refs/heads/different/"); got != "" {
				t.Fatalf("remote effects: %s", got)
			}
			if got := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname)", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts"); got != "" {
				t.Fatalf("receipt effects: %s", got)
			}
		})
	}
}

func TestOperationTransportFailureRemainsRemote(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	policy := operationPolicy(t, f)
	// The expected and configured destinations agree; only the Git transport
	// fails. Use a missing local remote, without network or credentials.
	policy.ExpectedRemoteURL = filepath.Join(f.root, "unavailable.git")
	checkedGit(t, f.clone, "remote", "set-url", "origin", policy.ExpectedRemoteURL)
	_, _, err := clientAt(t, f.clone).Claim(context.Background(), ClaimRequest{Task: "first-task", Worker: "worker-a", NoWorktree: true}, policy)
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "remote" || failure.Code != "remote" || failure.ExitCode != 6 {
		t.Fatalf("wrong transport classification: %v", err)
	}
	if got := checkedGit(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/tsuzuri/", "refs/heads/tsuzuri-assignments/"); got != "" {
		t.Fatalf("remote effects: %s", got)
	}
	if got := checkedGit(t, f.clone, "for-each-ref", "--format=%(refname)", "refs/tsuzuri/receipts", "refs/tsuzuri/assignment-receipts"); got != "" {
		t.Fatalf("receipt effects: %s", got)
	}
}

func TestOperationEnvironmentValidation(t *testing.T) {
	for _, env := range [][]string{{"missing-equals"}, {"=empty"}, {"A=1", "A=2"}, {"A=with\x00nul"}} {
		if _, _, _, err := operationInputs([]OperationOptions{{GitExecutable: "/git", Environment: env}}); err == nil {
			t.Fatalf("accepted malformed environment %q", env)
		}
	}
	if _, _, _, err := operationInputs([]OperationOptions{{Environment: []string{}}}); err == nil {
		t.Fatal("allowed ambient executable with explicit environment")
	}
	policy, env, _, err := operationInputs([]OperationOptions{{GitExecutable: "/git", Environment: []string{"GIT_DIR=wrong", "GIT_TRACE_CURL=1", "GIT_CURL_VERBOSE=1", "PATH=/bin", "GIT_CONFIG_COUNT=0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Environment != nil || strings.Join(env, "\n") != "PATH=/bin\nGIT_CONFIG_COUNT=0" {
		t.Fatalf("unsafe operation inputs: %v", env)
	}
}

func TestNativeMethodsRecoverOriginalRevision(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	client := clientAt(t, f.clone)
	policy := operationPolicy(t, f)
	ctx := context.Background()
	ready, source, err := client.Next(ctx, "example", policy)
	if err != nil || len(ready) != 1 || source.BaseObjectID == nil {
		t.Fatalf("next: %v %v", ready, err)
	}
	claim, _, err := client.Claim(ctx, ClaimRequest{Task: TaskID(ready[0].ID), Worker: "worker-a", ExpectedDigest: ready[0].RecordDigest, NoWorktree: true}, policy)
	if err != nil {
		t.Fatal(err)
	}
	// Independent clone has no local receipts; accept may recover only the exact worker.
	other := cloneFixture(t, f, "native-recovery")
	otherClient := clientAt(t, other)
	if _, _, err = otherClient.Accept(ctx, AcceptRequest{Task: "first-task", Worker: "worker-b", NoWorktree: true}, policy); err == nil {
		t.Fatal("wrong worker recovered")
	}
	accepted, _, err := otherClient.Accept(ctx, AcceptRequest{Task: "first-task", Worker: "worker-a", NoWorktree: true}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ClaimedTask == nil || *accepted.ClaimedTask != claim.ClaimedTask || accepted.ClaimObjectID != claim.Metadata.AnchorObjectID || accepted.Assignment.ObjectID != claim.Assignment.ObjectID || !accepted.CreatedLocalReceipt {
		t.Fatalf("recovery changed original authority: %#v", accepted)
	}
	shown, _, err := otherClient.Show(ctx, "first-task", policy)
	if err != nil || shown.Claim == nil || shown.Claim.Assignment.Metadata.ObjectID != accepted.Assignment.ObjectID {
		t.Fatalf("post-accept verification: %#v %v", shown, err)
	}
}

func TestOperationDiagnosticsRedactCredentialEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git")
	// Emulate transport diagnostics without making network requests. The secret
	// belongs in the child environment and must not survive the diagnostic boundary.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'remote: policy rejected Authorization: %s\\n' \"$DUMMY_TOKEN\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client := clientAt(t, t.TempDir())
	_, _, err := client.Show(context.Background(), "first-task", OperationOptions{GitExecutable: path, Environment: []string{"DUMMY_TOKEN=credential-never-log"}})
	if err == nil || strings.Contains(err.Error(), "credential-never-log") || !strings.Contains(err.Error(), "policy rejected") {
		t.Fatalf("unsafe/useless diagnostic: %v", err)
	}
}
