package tsuzuri

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real entry script against disposable Git inputs. No artifact
// build or module download should start for any rejected source identity.
func TestReleaseInputGuards(t *testing.T) {
	script, err := filepath.Abs("scripts/release-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, version, change, want string }{
		{"stable", "0.1.0", "", "VERSION must be a prerelease"},
		{"mismatch", "0.0.0-rc.0", "", "VERSION does not match"},
		{"modified", Version, "query.go", "committed and clean"},
		{"untracked", Version, "extra.go", "committed and clean"},
		{"compiler", Version, "", "compiler does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_DIR=", "GIT_WORK_TREE=")
			// Git treats empty GIT_DIR as a selection too; remove selectors.
			clean := env[:0]
			for _, value := range env {
				if !strings.HasPrefix(value, "GIT_DIR=") && !strings.HasPrefix(value, "GIT_WORK_TREE=") && !strings.HasPrefix(value, "GIT_COMMON_DIR=") && !strings.HasPrefix(value, "GIT_INDEX_FILE=") {
					clean = append(clean, value)
				}
			}
			run := func(args ...string) {
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir, cmd.Env = dir, clean
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v: %s", err, out)
				}
			}
			run("init", "-q")
			for path, body := range map[string]string{"query.go": "package tsuzuri\nconst Version = \"" + Version + "\"\n", ".go-version": "0.0.0\n"} {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run("add", ".")
			run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			if tc.change != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.change), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(ctx, "sh", script, tc.version)
			cmd.Dir, cmd.Env = dir, clean
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("got %v, %s; want %s", err, out, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "build")); !os.IsNotExist(err) {
				t.Fatalf("rejected inputs created artifacts: %v", err)
			}
		})
	}
}
