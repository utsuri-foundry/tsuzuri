package tsuzuri

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const historyTaskPath = "tasks/projects/example/tasks/first-task.task.md"

func historyCommit(t *testing.T, f testRepository) string {
	t.Helper()
	checkedGit(t, f.clone, "add", "-A")
	return checkedGit(t, f.clone, "commit", "--allow-empty", "-m", "history fixture")
}
func historyWrite(t *testing.T, f testRepository, path, body string) {
	t.Helper()
	writeTestFile(t, filepath.Join(f.clone, path), body)
	historyCommit(t, f)
}
func historyRemove(t *testing.T, f testRepository, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.clone, path)); err != nil {
		t.Fatal(err)
	}
	historyCommit(t, f)
}
func historyTerminal() string {
	return strings.ReplaceAll(testTask, `state = "ready"`, `state = "done"`) + "\n# Resolution\n\nDelivered.\n"
}

func TestHistoryReplay(t *testing.T) {
	isolatedGit(t)
	cases := []struct {
		name  string
		valid bool
		edit  func(*testing.T, testRepository)
	}{
		{"prose-reverts", true, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(testTask, "Useful change.", "Other change."))
			historyWrite(t, f, historyTaskPath, testTask)
		}},
		{"removed-id-reused", false, func(t *testing.T, f testRepository) {
			historyRemove(t, f, historyTaskPath)
			historyWrite(t, f, historyTaskPath, testTask)
		}},
		{"terminal-state-reopened-then-restored", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, historyTerminal())
			historyWrite(t, f, historyTaskPath, testTask)
			historyWrite(t, f, historyTaskPath, historyTerminal())
		}},
		{"terminal-removed-then-restored", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, historyTerminal())
			historyRemove(t, f, historyTaskPath)
			historyWrite(t, f, historyTaskPath, historyTerminal())
		}},
		{"terminal-prose-edit", true, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, historyTerminal())
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(historyTerminal(), "Delivered.", "Delivered and verified."))
		}},
		{"nonterminal-move", true, func(t *testing.T, f testRepository) { historyMove(t, f, false, false) }},
		{"terminal-move", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, historyTerminal())
			historyMove(t, f, true, false)
		}},
		{"duplicate-task", false, func(t *testing.T, f testRepository) {
			historyMove(t, f, false, true)
			historyRemove(t, f, "tasks/projects/second/tasks/first-task.task.md")
		}},
		{"malformed-task-then-fixed", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(testTask, `kind = "delivery"`, `kind = "invented"`))
			historyWrite(t, f, historyTaskPath, testTask)
		}},
		{"missing-acceptance-then-fixed", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(testTask, "# Acceptance", "# Something else"))
			historyWrite(t, f, historyTaskPath, testTask)
		}},
		{"malformed-project-then-fixed", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, "tasks/projects/example/project.md", "broken")
			historyWrite(t, f, "tasks/projects/example/project.md", testProject)
		}},
		{"malformed-config-then-fixed", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, ".tsuzuri.toml", "broken")
			historyWrite(t, f, ".tsuzuri.toml", testConfig)
		}},
		{"config-deleted-then-restored", false, func(t *testing.T, f testRepository) {
			historyRemove(t, f, ".tsuzuri.toml")
			historyWrite(t, f, ".tsuzuri.toml", testConfig)
		}},
		{"repository-id-changed-then-restored", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, ".tsuzuri.toml", strings.ReplaceAll(testConfig, "0198d2de", "1198d2de"))
			historyWrite(t, f, ".tsuzuri.toml", testConfig)
		}},
		{"executable-record", true, func(t *testing.T, f testRepository) { historyMode(t, f, "100755"); historyMode(t, f, "100644") }},
		{"symlink-record-then-fixed", false, func(t *testing.T, f testRepository) { historyMode(t, f, "120000"); historyMode(t, f, "100644") }},
		{"tab-in-record-path", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, "tasks/projects/example/tasks/odd\tname.task.md", testTask)
			historyRemove(t, f, "tasks/projects/example/tasks/odd\tname.task.md")
		}},
		{"newline-in-record-path", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, "tasks/projects/example/tasks/odd\nname.task.md", testTask)
			historyRemove(t, f, "tasks/projects/example/tasks/odd\nname.task.md")
		}},
		{"ignored-path-delimiters", true, func(t *testing.T, f testRepository) {
			historyWrite(t, f, "tasks/projects/example/odd\tname\n.txt", "unrelated")
		}},
		{"preexisting-root-switch", true, func(t *testing.T, f testRepository) { historyRootSwitch(t, f, false) }},
		{"terminal-root-switch", false, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, historyTerminal())
			historyRootSwitch(t, f, true)
		}},
		{"inactive-malformed-record", true, func(t *testing.T, f testRepository) {
			historyWrite(t, f, "backlog/example/tasks/bad.task.md", "broken")
			historyRemove(t, f, "backlog/example/tasks/bad.task.md")
			historyRootSwitch(t, f, false)
		}},
		{"adoption-with-existing-legacy-records", true, func(t *testing.T, f testRepository) { historyAdoption(t, f) }},
		{"side-branch-invalid-history", true, func(t *testing.T, f testRepository) { historySideMerge(t, f, false) }},
		{"merge-resolution-reopens-terminal", false, func(t *testing.T, f testRepository) { historySideMerge(t, f, true) }},
		// Historical validation deliberately does not impose the tip's cross-record
		// registry/dependency checks on each intermediate inventory.
		{"intermediate-missing-dependency", true, func(t *testing.T, f testRepository) {
			historyWrite(t, f, historyTaskPath, strings.ReplaceAll(testTask, "blocked_by = []", "blocked_by = [\"missing\"]"))
			historyWrite(t, f, historyTaskPath, testTask)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			tc.edit(t, f)
			checkedGit(t, f.clone, "push", "--force", "origin", "HEAD:main")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			r, err := clientAt(t, f.clone).session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			tip := checkedGit(t, f.clone, "rev-parse", "HEAD")
			err = r.validateHistory(ctx, tip, "0198d2de-18c7-7ca0-a327-e6b4760f8613")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			for _, name := range []string{"TSUZURI_HISTORY_BASELINE", "TSUZURI_RUST_BINARY"} {
				binary := os.Getenv(name)
				if binary == "" {
					continue
				}
				// Each reference reads exactly the same immutable history through its own clone.
				clone := cloneFixture(t, f, strings.ToLower(name))
				command := exec.CommandContext(ctx, binary, "status", "--json")
				command.Dir = clone
				output, e := command.CombinedOutput()
				wantValid := tc.valid
				// Recorded reference defects, not exceptions to candidate checks:
				// formatted ls-tree paths hide escaped suffixes in both references;
				// Rust's historical loader does not inspect record file modes.
				if tc.name == "tab-in-record-path" || tc.name == "newline-in-record-path" ||
					(tc.name == "symlink-record-then-fixed" && name == "TSUZURI_RUST_BINARY") {
					wantValid = true
				}
				if (e == nil) != wantValid {
					t.Fatalf("%s differs (want valid=%v): %v\n%s", name, wantValid, e, output)
				}
				if e != nil {
					exit, ok := e.(*exec.ExitError)
					if !ok || exit.ExitCode() != 3 {
						t.Fatalf("reference failed outside validation: %v\n%s", e, output)
					}
				}
			}
		})
	}
}
func historyMove(t *testing.T, f testRepository, terminalTask, keepOld bool) {
	t.Helper()
	body := testTask
	if terminalTask {
		body = historyTerminal()
	}
	writeTestFile(t, filepath.Join(f.clone, "tasks/projects/second/project.md"), strings.ReplaceAll(testProject, "example", "second"))
	writeTestFile(t, filepath.Join(f.clone, "tasks/projects/second/tasks/first-task.task.md"), strings.ReplaceAll(body, `project = "example"`, `project = "second"`))
	if !keepOld {
		if err := os.Remove(filepath.Join(f.clone, historyTaskPath)); err != nil {
			t.Fatal(err)
		}
	}
	historyCommit(t, f)
}
func historyMode(t *testing.T, f testRepository, mode string) {
	t.Helper()
	blob := checkedGit(t, f.clone, "rev-parse", "HEAD:"+historyTaskPath)
	checkedGit(t, f.clone, "update-index", "--cacheinfo", mode+","+blob+","+historyTaskPath)
	checkedGit(t, f.clone, "commit", "-m", "change record mode")
}
func historyRootSwitch(t *testing.T, f testRepository, terminalTask bool) {
	t.Helper()
	body := testTask
	if terminalTask {
		body = historyTerminal()
	}
	writeTestFile(t, filepath.Join(f.clone, "backlog/example/project.md"), testProject)
	writeTestFile(t, filepath.Join(f.clone, "backlog/example/tasks/first-task.task.md"), body)
	historyCommit(t, f)
	historyWrite(t, f, ".tsuzuri.toml", strings.ReplaceAll(testConfig, "tasks/projects", "backlog"))
}
func historyAdoption(t *testing.T, f testRepository) {
	t.Helper()
	checkedGit(t, f.clone, "checkout", "--orphan", "before-adoption")
	checkedGit(t, f.clone, "rm", "-rf", ".")
	writeTestFile(t, filepath.Join(f.clone, "tasks/projects/example/project.md"), testProject)
	legacy := strings.ReplaceAll(testTask, "kind = \"delivery\"\n", "")
	writeTestFile(t, filepath.Join(f.clone, historyTaskPath), legacy)
	historyCommit(t, f)
	historyWrite(t, f, ".tsuzuri.toml", testConfig)
	historyWrite(t, f, historyTaskPath, testTask)
}
func historySideMerge(t *testing.T, f testRepository, reopen bool) {
	t.Helper()
	checkedGit(t, f.clone, "checkout", "-b", "side")
	historyWrite(t, f, historyTaskPath, "broken")
	historyWrite(t, f, historyTaskPath, strings.ReplaceAll(testTask, "Useful change.", "Side change."))
	side := checkedGit(t, f.clone, "rev-parse", "HEAD")
	checkedGit(t, f.clone, "checkout", "main")
	if reopen {
		historyWrite(t, f, historyTaskPath, historyTerminal())
	} else {
		historyWrite(t, f, "other", "main change")
	}
	parent := checkedGit(t, f.clone, "rev-parse", "HEAD")
	tree := checkedGit(t, f.clone, "rev-parse", side+"^{tree}")
	merged := checkedGit(t, f.clone, "commit-tree", tree, "-p", parent, "-p", side, "-m", "merge resolution")
	checkedGit(t, f.clone, "reset", "--hard", merged)
	if reopen {
		historyWrite(t, f, historyTaskPath, historyTerminal())
	}
}

func TestHistoryBatchingAndProofScope(t *testing.T) {
	isolatedGit(t)
	f := historyFixture(t, 200, "add")
	wrapper, counter := historyGitCounter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := clientAt(t, f.clone).session(ctx, OperationOptions{GitExecutable: wrapper, Environment: append(os.Environ(), "HISTORY_COUNTER="+counter)})
	if err != nil {
		t.Fatal(err)
	}
	tip := checkedGit(t, f.clone, "rev-parse", "HEAD")
	if err = os.WriteFile(counter, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.validateHistory(ctx, tip, "0198d2de-18c7-7ca0-a327-e6b4760f8613"); err != nil {
		t.Fatal(err)
	}
	if n := countHistoryGit(t, counter); n > 20 {
		t.Fatalf("history returned to per-revision processes: %d", n)
	}
	if r.historyStats.ParsedRecords != 201 {
		t.Fatalf("unchanged records reparsed: %+v", r.historyStats)
	}
	calls := countHistoryGit(t, counter)
	if err = r.validateHistory(ctx, tip, "0198d2de-18c7-7ca0-a327-e6b4760f8613"); err != nil {
		t.Fatal(err)
	}
	if countHistoryGit(t, counter) != calls {
		t.Fatal("same-operation proof not reused")
	}
	checkedGit(t, f.clone, "checkout", "-b", "rewrite")
	historyWrite(t, f, historyTaskPath, historyTerminal())
	historyWrite(t, f, historyTaskPath, testTask)
	rewritten := checkedGit(t, f.clone, "rev-parse", "HEAD")
	if err = r.validateHistory(ctx, rewritten, "0198d2de-18c7-7ca0-a327-e6b4760f8613"); err == nil {
		t.Fatal("unverified rewritten base inherited proof")
	}
	if r.histories[rewritten+":0198d2de-18c7-7ca0-a327-e6b4760f8613"] {
		t.Fatal("failed validation cached proof")
	}
}

func TestHistoryFraming(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	valid := a + "\x00:000000 100644 " + zero + " " + b + " A\x00tasks/odd\tname\n.task.md\x00"
	got, err := parseHistoryDiff(valid, []string{a})
	if err != nil || len(got) != 1 || len(got[0].changes) != 1 {
		t.Fatalf("valid NUL framing: %#v %v", got, err)
	}
	for _, bad := range []string{"", valid[:len(valid)-1], valid + "extra", strings.Replace(valid, "100644", "999999", 1), strings.Replace(valid, " A\x00", " R100\x00", 1), strings.Replace(valid, " A\x00", " D\x00", 1), strings.Replace(valid, zero, b, 1)} {
		if _, err := parseHistoryDiff(bad, []string{a}); err == nil {
			t.Fatal("accepted malformed raw diff")
		}
	}
	for _, bad := range []string{"", a + " missing\n", a + " tree 3\n", a + " blob -1\n", a + " blob " + fmt.Sprint(maxRecord+1) + "\n", a + " blob 1\ntrailing"} {
		if _, err := historyBlobSizes(bad, []string{a}); err == nil {
			t.Fatal("accepted invalid batch metadata")
		}
	}
}
func FuzzHistoryFraming(f *testing.F) {
	id := strings.Repeat("a", 40)
	f.Add(id+"\x00", id+" blob 4\n")
	f.Add(id+"\x00:000000 100644 "+strings.Repeat("0", 40)+" "+id+" A\x00tasks/odd\tname\n.task.md\x00", id+" blob 4\n")
	f.Add("broken", "missing")
	f.Fuzz(func(t *testing.T, diff, objects string) {
		if len(diff)+len(objects) > 2<<20 {
			t.Skip()
		}
		_, _ = parseHistoryDiff(diff, []string{id})
		_, _ = historyBlobSizes(objects, []string{id})
	})
}

func TestHistoryAdoptionLiteralPathsAndPrefix(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	checkedGit(t, f.clone, "checkout", "--orphan", "new-root")
	checkedGit(t, f.clone, "rm", "-rf", ".")
	historyWrite(t, f, "old", "before adoption")
	before := checkedGit(t, f.clone, "rev-parse", "HEAD")
	writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), testConfig)
	writeTestFile(t, filepath.Join(f.clone, "tasks/projects/example/project.md"), testProject)
	writeTestFile(t, filepath.Join(f.clone, historyTaskPath), testTask)
	historyCommit(t, f)
	adoption := checkedGit(t, f.clone, "rev-parse", "HEAD")
	historyWrite(t, f, "unrelated", "after adoption")
	tip := checkedGit(t, f.clone, "rev-parse", "HEAD")
	r, err := clientAt(t, f.clone).session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const repoID = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
	if err = r.validateHistory(context.Background(), tip, repoID); err != nil {
		t.Fatal(err)
	}
	if r.histories[before+":"+repoID] || !r.histories[adoption+":"+repoID] || !r.histories[tip+":"+repoID] {
		t.Fatal("proof crosses adoption boundary or omits valid prefix")
	}
	// Bootstrap tree paths need the same literal handling as later diff paths.
	checkedGit(t, f.clone, "reset", "--hard", adoption)
	writeTestFile(t, filepath.Join(f.clone, "tasks/projects/example/tasks/odd\tname.task.md"), testTask)
	checkedGit(t, f.clone, "add", "-A")
	checkedGit(t, f.clone, "commit", "--amend", "--no-edit")
	historyRemove(t, f, "tasks/projects/example/tasks/odd\tname.task.md")
	tip = checkedGit(t, f.clone, "rev-parse", "HEAD")
	if err = r.validateHistory(context.Background(), tip, repoID); err == nil {
		t.Fatal("quoted path at adoption was hidden")
	}
}

func TestHistoryCurrentTreeLiteralRoot(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	root := "tâches"
	writeTestFile(t, filepath.Join(f.clone, root, "example/project.md"), testProject)
	writeTestFile(t, filepath.Join(f.clone, root, "example/tasks/first-task.task.md"), testTask)
	historyCommit(t, f)
	historyWrite(t, f, ".tsuzuri.toml", strings.ReplaceAll(testConfig, "tasks/projects", root))
	checkedGit(t, f.clone, "push", "origin", "main")
	tasks, _, err := clientAt(t, f.clone).Next(context.Background(), "")
	if err != nil || len(tasks) != 1 || tasks[0].Path != root+"/example/tasks/first-task.task.md" {
		t.Fatalf("literal current root lost records: %#v %v", tasks, err)
	}
}

func TestHistoryRootSwitchAcrossBatches(t *testing.T) {
	isolatedGit(t)
	f := historyFixture(t, 130, "root")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := clientAt(t, f.clone).session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const repoID = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
	if err = r.validateHistory(ctx, checkedGit(t, f.clone, "rev-parse", "HEAD"), repoID); err != nil {
		t.Fatal(err)
	}
	if r.historyStats.ParsedRecords != 2 || r.historyStats.Revisions != 130 {
		t.Fatalf("root transitions lost state or reparsed unchanged records: %+v", r.historyStats)
	}
	historyWrite(t, f, ".tsuzuri.toml", "broken")
	historyWrite(t, f, ".tsuzuri.toml", testConfig)
	if err = r.validateHistory(ctx, checkedGit(t, f.clone, "rev-parse", "HEAD"), repoID); err == nil {
		t.Fatal("configuration defect after a batch boundary was hidden")
	}
}
