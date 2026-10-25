package tsuzuri

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxOutput = 64 << 20
const maxRecord = 1 << 20
const maxRecords = 100000
const maxHistory = 100000
const commandTimeout = 30 * time.Second
const operationTimeout = 2 * time.Minute

// Client selects a repository. Each operation obtains its own context and snapshot.
// Concurrent callers must still coordinate clone-local authoring/worktree operations.
type Client struct{ directory string }

// Open selects a clone path. Repository validation occurs when an operation runs.
func Open(directory string) (*Client, error) {
	path, err := filepath.Abs(directory)
	if err != nil {
		return nil, invalid("repository path: %v", err).causedBy(err)
	}
	return &Client{directory: path}, nil
}

type repository struct {
	root, common, git string
	environment       []string
	policy            OperationOptions
	trees             map[string]*inventory
	histories         map[string]bool
	historyStats      historyStats
	cache             repositoryCache
}
type gitOutput struct {
	text, diagnostic string
	code             int
}

// Attach a failed push's safe diagnostic only after exact-ref reconciliation
// establishes an error outcome. A failed response alone does not prove failure.
func pushFailure(result *Error, output gitOutput, pushErr error) *Error {
	if pushErr == nil && output.code == 0 {
		return result
	}
	diagnostic := output.diagnostic
	if pushErr != nil {
		if diagnostic != "" {
			diagnostic += "\n"
		}
		diagnostic += pushErr.Error()
	}
	if diagnostic == "" {
		diagnostic = fmt.Sprintf("Git push exited %d", output.code)
	}
	diagnostic = redact(diagnostic)
	result.Message += ": " + diagnostic
	if pushErr == nil {
		pushErr = failure("remote", diagnostic)
	}
	return result.causedBy(errors.Join(result.cause, pushErr))
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.overflow = true
		b.cancel()
		return 0, fmt.Errorf("Git output exceeded bound")
	}
	return b.buffer.Write(p)
}

// Do not embed bytes.Buffer: its promoted ReadFrom method would let io.Copy
// bypass Write and therefore bypass the output limit.
func (b *cappedBuffer) Len() int       { return b.buffer.Len() }
func (b *cappedBuffer) String() string { return b.buffer.String() }
func (b *cappedBuffer) Bytes() []byte  { return b.buffer.Bytes() }

var credentialURL = regexp.MustCompile(`(https?://)[^/@\s]+@`)
var authHeader = regexp.MustCompile(`(?i)(authorization:)[^\r\n]+`)

func redact(s string) string {
	return authHeader.ReplaceAllString(credentialURL.ReplaceAllString(s, "${1}[redacted]@"), "${1} [redacted]")
}
func (r *repository) exec(ctx context.Context, input string, env []string, args ...string) (gitOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	fixed := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never"}
	cmd := exec.CommandContext(ctx, r.git, append(fixed, args...)...)
	cmd.Dir = r.root
	cmd.Env = append(append([]string{}, r.environment...), "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(input)
	out := &cappedBuffer{limit: maxOutput, cancel: cancel}
	diag := &cappedBuffer{limit: 64 << 10, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = diag
	cmd.WaitDelay = time.Second
	if err := configureProcess(cmd); err != nil {
		return gitOutput{}, err
	}
	err := cmd.Run()
	result := gitOutput{text: out.String(), diagnostic: redact(strings.TrimSpace(diag.String()))}
	if out.overflow || diag.overflow {
		return result, invalid("Git output exceeded bound")
	}
	if ctx.Err() != nil {
		return result, invalid("Git operation cancelled or timed out: %v", ctx.Err()).causedBy(ctx.Err())
	}
	if !utf8.Valid(out.Bytes()) {
		return result, invalid("Git returned non-UTF-8 output")
	}
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			result.code = e.ExitCode()
		} else {
			return result, invalid("could not run Git: %v", err).causedBy(err)
		}
	}
	return result, nil
}
func (r *repository) run(ctx context.Context, args ...string) (string, error) {
	return r.input(ctx, "", nil, args...)
}
func (r *repository) input(ctx context.Context, input string, env []string, args ...string) (string, error) {
	o, e := r.exec(ctx, input, env, args...)
	if e != nil {
		return "", e
	}
	if o.code != 0 {
		if o.diagnostic == "" {
			o.diagnostic = fmt.Sprintf("Git exited %d", o.code)
		}
		return "", invalid("%s", o.diagnostic)
	}
	return o.text, nil
}
func (r *repository) oid(ctx context.Context, rev string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", invalid("Git operation cancelled or timed out: %v", err).causedBy(err)
	}
	immutable := immutableRevision(rev)
	if value, ok := r.cache.resolutions[rev]; immutable && ok {
		return value, nil
	}
	s, e := r.run(ctx, "rev-parse", "--verify", rev)
	s = strings.TrimSpace(s)
	if e == nil && immutable && validObject(s) && len(r.cache.resolutions) < maxCacheEntries {
		if r.cache.resolutions == nil {
			r.cache.resolutions = map[string]string{}
		}
		r.cache.resolutions[strings.Clone(rev)] = strings.Clone(s)
	}
	return s, e
}
func (r *repository) ref(ctx context.Context, ref string) (string, error) {
	o, e := r.exec(ctx, "", nil, "show-ref", "--verify", "--quiet", ref)
	if e != nil {
		return "", e
	}
	if o.code == 1 {
		return "", nil
	}
	if o.code != 0 {
		return "", invalid("cannot read ref %s: %s", ref, o.diagnostic)
	}
	return r.oid(ctx, ref)
}
func (r *repository) blob(ctx context.Context, s string, write bool) (string, error) {
	a := []string{"hash-object"}
	if write {
		a = append(a, "-w")
	}
	a = append(a, "--stdin")
	v, e := r.input(ctx, s, nil, a...)
	return strings.TrimSpace(v), e
}
func (r *repository) show(ctx context.Context, commit, path string) (string, error) {
	return r.run(ctx, "show", commit+":"+path)
}
func (r *repository) update(ctx context.Context, ref, new, old string) error {
	_, e := r.run(ctx, "update-ref", ref, new, old)
	return e
}
func (r *repository) remove(ctx context.Context, ref, old string) error {
	_, e := r.run(ctx, "update-ref", "-d", ref, old)
	return e
}
func (r *repository) canonicalRoot(ctx context.Context) (string, error) {
	p, e := filepath.EvalSymlinks(r.common)
	if e != nil {
		return "", invalid("Git common directory: %v", e).causedBy(e)
	}
	if filepath.Base(p) != ".git" {
		return "", invalid("requires non-bare canonical checkout")
	}
	return filepath.Dir(p), nil
}
func (c *Client) session(ctx context.Context, options ...OperationOptions) (*repository, error) {
	policy, environment, path, e := operationInputs(options)
	if e != nil {
		return nil, e
	}
	r := &repository{root: c.directory, git: path, environment: environment, policy: policy, trees: map[string]*inventory{}, histories: map[string]bool{}}
	root, e := r.run(ctx, "rev-parse", "--show-toplevel")
	if e != nil {
		return nil, e
	}
	r.root = strings.TrimSpace(root)
	common, e := r.run(ctx, "rev-parse", "--git-common-dir")
	if e != nil {
		return nil, e
	}
	r.common = strings.TrimSpace(common)
	if !filepath.IsAbs(r.common) {
		r.common = filepath.Join(r.root, r.common)
	}
	return r, nil
}
func regularFile(path string) (text string, err error) {
	info, e := os.Lstat(path)
	if e != nil {
		return "", invalid("cannot inspect %s: %v", path, e).causedBy(e)
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecord {
		return "", invalid("record must be a regular file within %d bytes: %s", maxRecord, path)
	}
	f, e := openRecord(path)
	if e != nil {
		return "", invalid("cannot open record: %v", e).causedBy(e)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	opened, e := f.Stat()
	if e != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", invalid("record changed during read").causedBy(e)
	}
	b, e := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if e != nil || len(b) > maxRecord || !utf8.Valid(b) {
		return "", invalid("invalid or oversized UTF-8 record").causedBy(e)
	}
	return string(b), nil
}
func uuid() string {
	b := make([]byte, 16)
	// Go 1.26 crypto/rand.Read always fills b; failure terminates the process.
	rand.Read(b)
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
