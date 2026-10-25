//go:build linux || darwin

package tsuzuri

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in measurement, not a timing assertion. All mutations target fresh local
// fixtures. Binary paths are explicit so an installed operator is never replaced.
func TestHistoryProbe(t *testing.T) {
	if os.Getenv("TSUZURI_HISTORY_PROBE") != "1" {
		t.Skip("set TSUZURI_HISTORY_PROBE=1 for bounded measurements")
	}
	baseline, rust, candidate := os.Getenv("TSUZURI_HISTORY_BASELINE"), os.Getenv("TSUZURI_RUST_BINARY"), os.Getenv("TSUZURI_HISTORY_CANDIDATE")
	isolatedGit(t)
	sizes := []int{50, 200, 600}
	if s := os.Getenv("TSUZURI_HISTORY_SIZE"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 6000 {
			t.Fatal("invalid probe size")
		}
		sizes = []int{n}
	}
	mode := os.Getenv("TSUZURI_HISTORY_MODE")
	if mode == "" {
		mode = "add"
	}
	if os.Getenv("TSUZURI_HISTORY_SOURCE") != "" {
		sizes = []int{0}
		mode = "target"
	}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("%s-%d", mode, n), func(t *testing.T) {
			var f testRepository
			if source := os.Getenv("TSUZURI_HISTORY_SOURCE"); source != "" {
				f = historyTargetFixture(t, source)
			} else {
				f = historyFixture(t, n, mode)
			}
			wrapper, counter := historyGitCounter(t)
			policy := OperationOptions{GitExecutable: wrapper, Environment: append(os.Environ(), "HISTORY_COUNTER="+counter)}
			tip := checkedGit(t, f.clone, "rev-parse", "HEAD")
			t.Logf("fixture head=%s mode=%s", tip, mode)
			for trial := 0; trial < 3; trial++ {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				r, err := clientAt(t, f.clone).session(ctx, policy)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				cfg, err := r.config(ctx)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				if err = os.WriteFile(counter, nil, 0600); err != nil {
					cancel()
					t.Fatal(err)
				}
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := time.Now()
				err = r.validateHistory(ctx, tip, cfg.RepositoryID)
				elapsed := time.Since(start)
				cancel()
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				parsed := 0
				for key, tree := range r.trees {
					if strings.HasSuffix(key, ":true") {
						parsed += len(tree.Tasks) + len(tree.Projects)
					}
				}
				t.Logf("scan n=%d mode=%s trial=%d ms=%.3f processes=%d allocated_bytes=%d legacy_inventory_records=%d parsed_records=%d blob_bytes=%d", n, mode, trial, float64(elapsed.Microseconds())/1000, countHistoryGit(t, counter), after.TotalAlloc-before.TotalAlloc, parsed, r.historyStats.ParsedRecords, r.historyStats.BlobBytes)
			}
			for _, b := range []struct{ name, path string }{{"baseline", baseline}, {"rust", rust}, {"candidate", candidate}} {
				if b.path == "" {
					continue
				}
				clone := cloneFixture(t, f, b.name)
				for _, command := range []string{"status", "overview"} {
					for trial := 0; trial < 3; trial++ {
						if err := os.WriteFile(counter, nil, 0600); err != nil {
							t.Fatal(err)
						}
						ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
						cmd := exec.CommandContext(ctx, b.path, command, "--json")
						rssPath := filepath.Join(f.root, "rss")
						if timer := os.Getenv("TSUZURI_HISTORY_TIME"); timer != "" {
							cmd = exec.CommandContext(ctx, timer, "-f", "%M", "-o", rssPath, "--", b.path, command, "--json")
						}
						cmd.Dir = clone
						cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"), "HISTORY_COUNTER="+counter)
						if err := configureProcess(cmd); err != nil {
							cancel()
							t.Fatal(err)
						}
						cmd.WaitDelay = time.Second
						stdout := &cappedBuffer{limit: maxOutput, cancel: cancel}
						stderr := &cappedBuffer{limit: 64 << 10, cancel: cancel}
						cmd.Stdout, cmd.Stderr = stdout, stderr
						start := time.Now()
						err := cmd.Run()
						out := stdout.Bytes()
						elapsed := time.Since(start)
						cancel()
						if err != nil {
							t.Fatalf("%s %s: %v", b.name, command, err)
						}
						var response struct {
							OK bool `json:"ok"`
						}
						if err = json.Unmarshal(out, &response); err != nil || !response.OK {
							t.Fatalf("invalid response %s: %v", b.name, err)
						}
						rss := int64(-1)
						if os.Getenv("TSUZURI_HISTORY_TIME") != "" {
							body, e := os.ReadFile(rssPath)
							if e != nil {
								t.Fatal(e)
							}
							rss, e = strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
							if e != nil {
								t.Fatal(e)
							}
						}
						t.Logf("cli=%s command=%s n=%d mode=%s trial=%d ms=%.3f processes=%d max_rss_kib=%d", b.name, command, n, mode, trial, float64(elapsed.Microseconds())/1000, countHistoryGit(t, counter), rss)
					}
				}
			}
		})
	}
}

func historyTargetFixture(t *testing.T, source string) testRepository {
	t.Helper()
	if !filepath.IsAbs(source) {
		t.Fatal("target source must be an absolute local path")
	}
	root := t.TempDir()
	remote, clone := filepath.Join(root, "target.git"), filepath.Join(root, "operator")
	checkedGit(t, root, "clone", "--bare", "--no-hardlinks", "--single-branch", "--no-tags", "--branch", "main", source, remote)
	checkedGit(t, root, "clone", remote, clone)
	configureClone(t, clone)
	return testRepository{root, remote, clone}
}

func historyGitCounter(t *testing.T) (string, string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	counter := filepath.Join(dir, "calls")
	// The real executable path is supplied as one quoted shell word, never taken
	// from fixture content. This wrapper only counts; it does not change Git args.
	quoted := "'" + strings.ReplaceAll(real, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HISTORY_COUNTER\"\nexec " + quoted + " \"$@\"\n"
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, counter
}
func countHistoryGit(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func historyFixture(t *testing.T, n int, mode string) testRepository {
	t.Helper()
	f := fixture(t)
	base := checkedGit(t, f.clone, "rev-parse", "HEAD")
	var input strings.Builder
	for i := 1; i < n; i++ {
		fmt.Fprintf(&input, "commit refs/heads/main\ncommitter Test <test@invalid> %d +0000\ndata 7\nfixture\n", 1700000000+i)
		if i == 1 {
			fmt.Fprintf(&input, "from %s\n", base)
		}
		switch mode {
		case "add":
			id := fmt.Sprintf("task-%06d", i)
			body := strings.ReplaceAll(testTask, "first-task", id)
			fmt.Fprintf(&input, "M 100644 inline tasks/projects/example/tasks/%s.task.md\ndata %d\n%s\n", id, len(body), body)
		case "edit":
			body := strings.ReplaceAll(testTask, "Useful change.", fmt.Sprintf("Revision %d.", i))
			fmt.Fprintf(&input, "M 100644 inline tasks/projects/example/tasks/first-task.task.md\ndata %d\n%s\n", len(body), body)
		case "unrelated":
			fmt.Fprintf(&input, "M 100644 inline unrelated\ndata %d\n%d\n", len(strconv.Itoa(i)), i)
		case "root":
			root := "tasks/projects"
			if i%2 == 1 {
				root = "backlog"
			}
			config := strings.ReplaceAll(testConfig, "tasks/projects", root)
			for _, record := range []struct{ path, body string }{
				{".tsuzuri.toml", config},
				{root + "/example/project.md", testProject},
				{root + "/example/tasks/first-task.task.md", testTask},
			} {
				fmt.Fprintf(&input, "M 100644 inline %s\ndata %d\n%s\n", record.path, len(record.body), record.body)
			}
		default:
			t.Fatal("unknown probe mode")
		}
		input.WriteByte('\n')
	}
	if n > 1 {
		cmd := exec.Command("git", "-c", "core.hooksPath=/dev/null", "fast-import", "--quiet")
		cmd.Dir = f.clone
		cmd.Stdin = strings.NewReader(input.String())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fast-import: %v %s", err, out)
		}
		checkedGit(t, f.clone, "reset", "--hard", "main")
		checkedGit(t, f.clone, "push", "origin", "main")
	}
	return f
}

func TestHistoryMissingAndTruncatedObjects(t *testing.T) {
	id := strings.Repeat("a", 40)
	for _, tc := range []struct{ name, check, body, want string }{
		{"missing", id + " missing\n", "", "missing or invalid history blob"},
		{"oversize", id + " blob 1048577\n", "", "history blob exceeds record bound"},
		{"truncated", id + " blob 4\n", id + " blob 4\nabc", "invalid Git object size"},
		{"trailing", id + " blob 4\n", id + " blob 4\nbody\nextra", "trailing Git object output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := filepath.Join(dir, "git")
			script := "#!/bin/sh\ncase \"$6\" in --batch-check) printf '%s' \"$CHECK\";; --batch) printf '%s' \"$BODY\";; *) exit 19;; esac\n"
			if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			r := repository{root: dir, git: fake, environment: []string{"CHECK=" + tc.check, "BODY=" + tc.body}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			visited := false
			err := r.historyBlobs(ctx, []string{id}, func(string, string) error { visited = true; return nil })
			if err == nil || visited || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid objects reached replay: visited=%v err=%v", visited, err)
			}
		})
	}
}
