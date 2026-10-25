//go:build linux || darwin

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const repositoryID = "0198d2de-18c7-7ca0-a327-e6b4760f8613"
const taskRoot = "tasks/projects/tsuzuri/tasks/"
const claimRef = "refs/heads/tsuzuri/tasks/first-task"
const assignmentRef = "refs/heads/tsuzuri/tasks-assignments/first-task"

var binary string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) (status int) {
	directory, err := os.MkdirTemp("", "tsuzuri-cli-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			status = 1
		}
	}()
	binary = os.Getenv("TSUZURI_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(directory, "tsuzuri")
		args := []string{"build", "-buildvcs=false", "-o", binary}
		if raceEnabled {
			args = append(args, "-race")
		}
		args = append(args, "../../cmd/tsuzuri")
		result := execute(context.Background(), ".", os.Environ(), "", "go", args...)
		if result.err != nil {
			fmt.Fprintf(os.Stderr, "build CLI: %v\n%s", result.err, result.stderr)
			return 1
		}
	} else if !filepath.IsAbs(binary) {
		fmt.Fprintln(os.Stderr, "TSUZURI_TEST_BINARY must be absolute")
		return 1
	}
	return m.Run()
}

type output struct {
	stdout, stderr string
	code           int
	err            error
}
type limitedBuffer struct{ bytes []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.bytes)+len(p) > 4<<20 {
		return 0, fmt.Errorf("test subprocess output exceeded 4 MiB")
	}
	b.bytes = append(b.bytes, p...)
	return len(p), nil
}
func execute(parent context.Context, directory string, environment []string, input, program string, args ...string) output {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = directory, environment, strings.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	return output{string(stdout.bytes), string(stderr.bytes), code, err}
}

type fixture struct {
	t                   *testing.T
	root, clone, remote string
	env                 []string
}

func newFixture(t *testing.T, writers ...func(*fixture)) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, clone: filepath.Join(root, "main"), remote: filepath.Join(root, "coordination.git")}
	f.env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root, "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file",
		"GIT_AUTHOR_NAME=Tsuzuri", "GIT_AUTHOR_EMAIL=tsuzuri@invalid",
		"GIT_COMMITTER_NAME=Tsuzuri", "GIT_COMMITTER_EMAIL=tsuzuri@invalid",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=init.templateDir", "GIT_CONFIG_VALUE_1=",
		"GORACE=atexit_sleep_ms=0",
	}
	mkdir(t, f.clone)
	f.git(root, "init", "--bare", f.remote)
	f.git(f.clone, "init", "-b", "main")
	f.identity(f.clone)
	f.writeRepository()
	for _, writer := range writers {
		writer(f)
	}
	f.git(f.clone, "add", ".")
	f.git(f.clone, "commit", "-m", "initialize fixture")
	f.git(f.clone, "remote", "add", "origin", f.remote)
	f.git(f.clone, "push", "-u", "origin", "main")
	f.configure(f.clone, "test-owner")
	return f
}
func (f *fixture) run(dir, input, program string, args ...string) output {
	return execute(f.t.Context(), dir, f.env, input, program, args...)
}
func (f *fixture) cli(dir string, args ...string) output { return f.run(dir, "", binary, args...) }
func (f *fixture) ok(dir string, args ...string) any {
	f.t.Helper()
	result := f.cli(dir, append([]string{"--json"}, args...)...)
	success(f.t, result)
	return body(f.t, result)
}
func (f *fixture) author(dir, input string, args ...string) output {
	return f.run(dir, input, binary, append([]string{"--json"}, args...)...)
}
func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	result := f.run(dir, "", "git", args...)
	success(f.t, result)
	return strings.TrimSpace(result.stdout)
}
func (f *fixture) gitInput(dir, input string, args ...string) string {
	f.t.Helper()
	result := f.run(dir, input, "git", args...)
	success(f.t, result)
	return strings.TrimSpace(result.stdout)
}
func (f *fixture) identity(dir string) {
	f.git(dir, "config", "user.name", "Tsuzuri Test")
	f.git(dir, "config", "user.email", "tsuzuri-test@invalid")
}
func (f *fixture) configure(dir, owner string) {
	f.git(dir, "config", "tsuzuri.remote", "origin")
	f.git(dir, "config", "tsuzuri.owner", owner)
}
func (f *fixture) independentClone() string {
	dir := filepath.Join(f.root, "second-clone")
	f.git(f.root, "clone", "--branch", "main", f.remote, dir)
	f.identity(dir)
	f.configure(dir, "second-owner")
	return dir
}
func (f *fixture) worktree(branch string) string {
	dir := filepath.Join(f.root, "wt-"+branch)
	f.git(f.clone, "worktree", "add", "-b", branch, dir, "main")
	return dir
}
func (f *fixture) commitPush(message string) {
	f.git(f.clone, "add", ".")
	f.git(f.clone, "commit", "-m", message)
	f.git(f.clone, "push", "origin", "main")
}
func (f *fixture) writeRepository() {
	write(f.t, filepath.Join(f.clone, ".tsuzuri.toml"), fmt.Sprintf("schema = 1\nrepository_id = %q\ntask_root = \"tasks/projects\"\nbase_branch = \"main\"\nclaim_namespace = \"tsuzuri/tasks\"\nworktree_root = \"..\"\nworktree_template = \"wt-{task_id}\"\nareas = [\"core\"]\nrisks = [\"coordination\"]\n", repositoryID))
	write(f.t, filepath.Join(f.clone, "tasks/projects/tsuzuri/project.md"), "+++\nschema = 1\nid = \"tsuzuri\"\ntitle = \"Tsuzuri\"\nstate = \"active\"\n+++\n\n# Goal\n\nCoordinate work safely.\n\n# Finish line\n\nThe protocol works.\n")
	f.task("first-task", readyTask("first-task", "p0", "delivery"))
	f.task("second-task", readyTask("second-task", "p1", "defect"))
}
func (f *fixture) task(id, content string) {
	write(f.t, filepath.Join(f.clone, taskRoot, id+".task.md"), content)
}
func readyTask(id, priority, kind string) string {
	return strings.ReplaceAll(nonterminalTask(id, "tsuzuri", "ready", priority, nil, ""), "kind = \"delivery\"", fmt.Sprintf("kind = %q", kind))
}
func nonterminalTask(id, project, state, priority string, dependencies []string, reason string) string {
	quoted := make([]string, len(dependencies))
	for i, d := range dependencies {
		quoted[i] = strconv.Quote(d)
	}
	extra := ""
	if reason != "" {
		extra = fmt.Sprintf("blocked_reason = %q\n", reason)
	}
	return fmt.Sprintf("+++\nschema = 1\nid = %q\nproject = %q\ntitle = %q\nkind = \"delivery\"\nstate = %q\npriority = %q\nblocked_by = [%s]\nareas = [\"core\"]\nrisks = [\"coordination\"]\ncreated = 2026-08-21\n%s+++\n\n# Outcome\n\nThe outcome exists.\n\n# Acceptance\n\n- The task passes.\n", id, project, id, state, priority, strings.Join(quoted, ", "), extra)
}
func terminalTask(id, priority, state string) string {
	s := nonterminalTask(id, "tsuzuri", state, priority, nil, "")
	return strings.Split(s, "# Outcome")[0] + "# Resolution\n\nThe task was completed.\n"
}
func (f *fixture) noWorkspace(dir string) {
	f.t.Helper()
	equal(f.t, f.git(dir, "for-each-ref", "--format=%(refname)", "refs/heads/tsuzuri/tasks"), "")
	equal(f.t, strings.Count(f.git(dir, "worktree", "list", "--porcelain"), "worktree "), 1)
}
func (f *fixture) hook(name, script string) {
	executable(f.t, filepath.Join(f.remote, "hooks", name), script)
}
func (f *fixture) envWith(overrides map[string]string) []string {
	env := make([]string, 0, len(f.env)+len(overrides))
	for _, entry := range f.env {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := overrides[key]; !ok {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func executable(t *testing.T, path, content string) {
	t.Helper()
	write(t, path, content)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}
func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
func rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}
func exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	equal(t, err == nil, want)
}
func pathType(t *testing.T, path string, directory bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if directory {
		equal(t, info.IsDir(), true)
	} else {
		equal(t, info.Mode().IsRegular(), true)
	}
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func success(t *testing.T, r output) {
	t.Helper()
	if r.err != nil || r.code != 0 {
		t.Fatalf("exit %d: %v\n%s\n%s", r.code, r.err, r.stdout, r.stderr)
	}
}
func body(t *testing.T, r output) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("JSON: %v\n%s\n%s", err, r.stdout, r.stderr)
	}
	return v
}
func field(t *testing.T, v any, path string) any {
	t.Helper()
	for _, key := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[key]
			if !ok {
				t.Fatalf("missing %s in %v", path, x)
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(x) {
				t.Fatalf("invalid index %s in %v", path, x)
			}
			v = x[i]
		default:
			t.Fatalf("cannot select %s from %v", path, v)
		}
	}
	return v
}
func textField(t *testing.T, v any, path string) string {
	t.Helper()
	s, ok := field(t, v, path).(string)
	if !ok {
		t.Fatalf("%s is not a string", path)
	}
	return s
}
func equal(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("got %s; want %s", a, b)
	}
}
func check(t *testing.T, v any, expected map[string]any) {
	t.Helper()
	for path, want := range expected {
		equal(t, map[string]any{path: field(t, v, path)}, map[string]any{path: want})
	}
}
func contains(t *testing.T, s, part string, want bool) {
	t.Helper()
	if strings.Contains(s, part) != want {
		t.Fatalf("contains %q = %t: %s", part, !want, s)
	}
}
func arrayContains(t *testing.T, v any, path, want string) {
	t.Helper()
	values, ok := field(t, v, path).([]any)
	if !ok {
		t.Fatalf("%s is not an array", path)
	}
	for _, x := range values {
		if x == want {
			return
		}
	}
	t.Fatalf("%s missing %q: %v", path, want, values)
}
func arrayLength(t *testing.T, v any, path string, want int) {
	t.Helper()
	a, ok := field(t, v, path).([]any)
	if !ok {
		t.Fatalf("%s is not an array", path)
	}
	equal(t, len(a), want)
}
func jsonError(t *testing.T, r output, code int, kind string) any {
	t.Helper()
	equal(t, r.code, code)
	v := body(t, r)
	check(t, v, map[string]any{"schema": 1, "ok": false, "error.kind": kind, "error.exitCode": code})
	return v
}
