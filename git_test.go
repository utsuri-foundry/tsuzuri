//go:build linux || darwin

package tsuzuri

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGitCancellationAndOutputBound(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
		want         string
	}{
		{"descendants", "#!/bin/sh\nsleep 30 &\nwait\n", 100 * time.Millisecond, "cancelled"},
		{"output", "#!/bin/sh\nexec yes bounded-output\n", 10 * time.Second, "exceeded bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := filepath.Join(dir, "git")
			if e := os.WriteFile(fake, []byte(tc.script), 0700); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			r := repository{root: dir, git: fake, environment: os.Environ()}
			start := time.Now()
			_, e := r.exec(ctx, "", nil, "status")
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("wanted %s: %v", tc.want, e)
			}
			if tc.name == "descendants" && !errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("lost cancellation cause: %v", e)
			}
			if time.Since(start) > tc.timeout+2*time.Second {
				t.Fatal("Git cancellation did not bound descendants and pipes")
			}
		})
	}
}

func TestGitCauseDoesNotChangeJSON(t *testing.T) {
	err := failure("remote", "safe diagnostic").causedBy(errors.New("private transport detail"))
	body, e := json.Marshal(err)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "cause") {
		t.Fatalf("cause exposed: %s", body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := repository{root: t.TempDir(), git: "git"}
	_, e = r.exact(ctx, "unused", "refs/heads/main")
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("lost nested cause: %v", e)
	}
}
func TestDiagnosticsRedactCredentials(t *testing.T) {
	original := "fatal: https://worker:super-secret@example.test/repo Authorization: bearer token-secret\n"
	redacted := redact(original)
	for _, secret := range []string{"super-secret", "token-secret", "worker:"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("credential leaked: %q", redacted)
		}
	}
	if !strings.Contains(redacted, "example.test/repo") {
		t.Fatal("lost useful diagnostic context")
	}
}
func TestJSONFingerprintEscapes(t *testing.T) {
	for _, s := range []string{"actual \u2028 \u2029", `literal \u2028 \u2029`, "backslash \\\u2028"} {
		encoded, e := compactJSON(s)
		if e != nil {
			t.Fatal(e)
		}
		var decoded string
		if e = decodeJSON(encoded, &decoded); e != nil {
			t.Fatal(e)
		}
		if decoded != s {
			t.Fatalf("canonical JSON changed input: %q to %q", s, decoded)
		}
	}
}
func TestIdentityProtocolBounds(t *testing.T) {
	for _, s := range []string{"worker/a", "agent-123_4@example.test", strings.Repeat("a", 128)} {
		if !validIdentity(s) {
			t.Fatalf("valid worker rejected: %q", s)
		}
	}
	for _, s := range []string{"", " leading", "worker space", "worker\nother", strings.Repeat("a", 129), "équipe"} {
		if validIdentity(s) {
			t.Fatalf("invalid worker accepted: %q", s)
		}
	}
}

func TestCancelledClaimInspectionDoesNotReportSuccess(t *testing.T) {
	testCancelledInspection(t, "log --format=%H%x09%s")
}

func TestCancelledHistoryScanDoesNotReportSuccess(t *testing.T) {
	testCancelledInspection(t, "diff-tree --stdin")
}

func testCancelledInspection(t *testing.T, stage string) {
	t.Helper()
	isolatedGit(t)
	f := fixture(t)
	client := clientAt(t, f.clone)
	mustClaim(t, client, "worker-a")
	policy := operationPolicy(t, f)
	markerPath := filepath.Join(t.TempDir(), "observing")
	if err := syscall.Mkfifo(markerPath, 0600); err != nil {
		t.Fatal(err)
	}
	marker, err := os.OpenFile(markerPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer marker.Close()
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \"$*\" in *'" + stage + "'*) printf ready > \"$CANCEL_MARKER\"; exec sleep 30;; esac\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	policy.Environment = append(policy.Environment, "REAL_GIT="+policy.GitExecutable, "CANCEL_MARKER="+markerPath)
	policy.GitExecutable = wrapper
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := client.Show(ctx, "first-task", policy); done <- err }()
	ready := make(chan error, 1)
	go func() { _, err := io.ReadFull(marker, make([]byte, 5)); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("inspection never reached selected observation: %v", err)
	case <-ctx.Done():
		t.Fatal("inspection did not reach observation within bound")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled observation reported success or lost cause: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inspection did not terminate")
	}
}
