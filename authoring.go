package tsuzuri

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CreateProjectInput is the JSON body accepted by create-project.
type CreateProjectInput struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	State      string         `json:"state"`
	Goal       string         `json:"goal"`
	FinishLine string         `json:"finishLine"`
	Extensions map[string]any `json:"extensions"`
}

// CreateTaskInput is the JSON body accepted by create-task.
type CreateTaskInput struct {
	ID                 string            `json:"id"`
	Project            string            `json:"project"`
	Title              string            `json:"title"`
	Kind               string            `json:"kind"`
	State              string            `json:"state"`
	Priority           string            `json:"priority"`
	BlockedBy          []string          `json:"blockedBy"`
	Areas              []string          `json:"areas"`
	Risks              []string          `json:"risks"`
	Created            string            `json:"created"`
	BlockedReason      *string           `json:"blockedReason"`
	Outcome            *string           `json:"outcome"`
	Acceptance         *string           `json:"acceptance"`
	Resolution         *string           `json:"resolution"`
	AdditionalSections map[string]string `json:"additionalSections"`
	Extensions         map[string]any    `json:"extensions"`
}

// TransitionTaskInput guards a task transition with its expected state and digest.
type TransitionTaskInput struct {
	ExpectedState  string  `json:"expectedState"`
	ExpectedDigest string  `json:"expectedDigest"`
	State          string  `json:"state"`
	BlockedReason  *string `json:"blockedReason"`
	Resolution     *string `json:"resolution"`
}
type operationReceipt struct {
	Schema             int             `json:"schema"`
	Operation          string          `json:"operation"`
	RequestID          string          `json:"requestId"`
	RequestFingerprint string          `json:"requestFingerprint"`
	WorktreeRef        string          `json:"worktreeRef"`
	Status             string          `json:"status"`
	Path               string          `json:"path"`
	ExpectedDigest     *string         `json:"expectedDigest"`
	DesiredDigest      string          `json:"desiredDigest"`
	Result             json.RawMessage `json:"result"`
}

func decodeJSON(input []byte, target any) error {
	if len(input) > maxRecord {
		return invalid("JSON input exceeds record bound")
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	d.UseNumber()
	if e := d.Decode(target); e != nil {
		return invalid("invalid JSON input: %v", e).causedBy(e)
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return invalid("JSON input must contain exactly one value")
	}
	return nil
}
func compactJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, invalid("cannot encode JSON: %v", err).causedBy(err)
	}
	// Protocol fingerprints keep U+2028/U+2029 literal. Consume complete escape
	// pairs so a literal backslash followed by u2028 stays literal.
	encoded := bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
	out := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			if i+6 <= len(encoded) && (string(encoded[i:i+6]) == `\u2028` || string(encoded[i:i+6]) == `\u2029`) {
				if encoded[i+5] == '8' {
					out = append(out, []byte("\u2028")...)
				} else {
					out = append(out, []byte("\u2029")...)
				}
				i += 5
				continue
			}
			out = append(out, encoded[i], encoded[i+1])
			i++
			continue
		}
		out = append(out, encoded[i])
	}
	return out, nil
}

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Author applies JSON record changes. Receipts and transition locks
// are clone-local Git refs; canonical task completion still requires integration.
func (c *Client) Author(ctx context.Context, command, id, requestID string, input []byte, options ...OperationOptions) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	r, e := c.session(ctx, options...)
	if e != nil {
		return Result{}, e
	}
	canonical, e := r.canonicalRoot(ctx)
	if e != nil {
		return Result{}, e
	}
	selected, e := filepath.EvalSymlinks(r.root)
	if e != nil {
		return Result{}, invalid("worktree path: %v", e).causedBy(e)
	}
	if selected == canonical {
		return Result{}, failure("canonical-checkout", "authoring and transition operations refuse canonical base checkout")
	}
	if !requestPattern.MatchString(requestID) || strings.Contains(requestID, "..") || strings.HasSuffix(requestID, ".") {
		return Result{}, failure("usage", "invalid request identity")
	}
	branch, e := r.run(ctx, "symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return Result{}, invalid("authoring requires named worktree branch")
	}
	branch = strings.TrimSpace(branch)
	cfg, e := r.config(ctx)
	if e != nil {
		return Result{}, e
	}
	var typed any
	var desired string
	var project CreateProjectInput
	var task CreateTaskInput
	var transition TransitionTaskInput
	switch command {
	case "create-project":
		if e = decodeJSON(input, &project); e != nil {
			return Result{}, e
		}
		if project.Extensions == nil {
			project.Extensions = map[string]any{}
		}
		typed = project
		desired, e = renderProject(project)
	case "create-task":
		if e = decodeJSON(input, &task); e != nil {
			return Result{}, e
		}
		if task.Extensions == nil {
			task.Extensions = map[string]any{}
		}
		if task.AdditionalSections == nil {
			task.AdditionalSections = map[string]string{}
		}
		typed = task
		desired, e = renderTask(task)
	case "transition-task":
		if !validID(id) {
			return Result{}, failure("usage", "invalid task identifier")
		}
		if e = decodeJSON(input, &transition); e != nil {
			return Result{}, e
		}
		typed = []any{id, transition}
	default:
		return Result{}, failure("usage", "unknown authoring command")
	}
	if e != nil {
		return Result{}, e
	}
	canonicalInput, e := compactJSON(typed)
	if e != nil {
		return Result{}, e
	}
	fingerprint, e := r.blob(ctx, string(canonicalInput), false)
	if e != nil {
		return Result{}, e
	}
	ref := "refs/tsuzuri/operations/" + cfg.RepositoryID + "/" + requestID
	stored, receipt, e := r.readOperation(ctx, ref)
	if e != nil {
		return Result{}, e
	}
	if stored != "" {
		if receipt.Operation != command || receipt.RequestFingerprint != fingerprint || receipt.WorktreeRef != branch {
			return Result{}, failure("idempotency-conflict", "request already bound to a different operation, input, or worktree")
		}
		if receipt.Status == "completed" {
			return r.authorResult(ctx, cfg, receipt, true)
		}
		if command == "transition-task" {
			current, e := regularFile(filepath.Join(r.root, receipt.Path))
			if e != nil {
				return Result{}, e
			}
			digest, e := r.blob(ctx, current, false)
			if e != nil {
				return Result{}, e
			}
			if receipt.ExpectedDigest != nil && digest == *receipt.ExpectedDigest {
				desired, e = transitionDocument(current, transition)
				if e != nil {
					return Result{}, e
				}
			}
		}
		return r.reconcileOperation(ctx, cfg, ref, stored, receipt, desired)
	}
	i, e := r.worktree(ctx)
	if e != nil {
		return Result{}, e
	}
	var path string
	var record any
	var expected *string
	switch command {
	case "create-project":
		path = cfg.TaskRoot + "/" + project.ID + "/project.md"
		if _, ok := i.Projects[project.ID]; ok {
			return Result{}, failure("idempotency-conflict", "project ID already exists")
		}
		p, e := parseProject(desired, path)
		if e != nil {
			return Result{}, e
		}
		i.Projects[p.ID] = p
		record = p
	case "create-task":
		path = cfg.TaskRoot + "/" + task.Project + "/tasks/" + task.ID + ".task.md"
		if _, ok := i.Tasks[task.ID]; ok {
			return Result{}, failure("idempotency-conflict", "task ID already exists")
		}
		t, e := parseTask(desired, path, false)
		if e != nil {
			return Result{}, e
		}
		i.Tasks[t.ID] = t
		record = t
	case "transition-task":
		t, ok := i.Tasks[id]
		if !ok {
			return Result{}, failure("usage", "unknown task "+id)
		}
		path = t.Path
		if t.State != transition.ExpectedState || i.Digests[path] != transition.ExpectedDigest || terminal(t.State) {
			return Result{}, failure("stale-input", "task does not match expected nonterminal state and content digest").details(map[string]any{"taskId": id, "expectedState": transition.ExpectedState, "actualState": t.State, "expectedDigest": transition.ExpectedDigest, "actualDigest": i.Digests[path]})
		}
		desired, e = transitionDocument(i.Documents[path], transition)
		if e != nil {
			return Result{}, e
		}
		t, e = parseTask(desired, path, false)
		if e != nil {
			return Result{}, e
		}
		i.Tasks[id] = t
		record = t
		expected = &transition.ExpectedDigest
	}
	if e = i.validate(); e != nil {
		return Result{}, e
	}
	if expected == nil {
		if _, e = os.Lstat(filepath.Join(r.root, path)); e == nil {
			return Result{}, failure("idempotency-conflict", "record path already exists")
		} else if !os.IsNotExist(e) {
			return Result{}, invalid("record path: %v", e).causedBy(e)
		}
	}
	digest, e := r.blob(ctx, desired, false)
	if e != nil {
		return Result{}, e
	}
	head, e := r.oid(ctx, "HEAD^{commit}")
	if e != nil {
		return Result{}, e
	}
	result := map[string]any{"requestId": requestID, "requestFingerprint": fingerprint, "repositoryId": cfg.RepositoryID, "headObjectId": head, "worktreeRef": branch, "path": path, "contentDigest": digest, "record": record}
	if expected != nil {
		result["previousDigest"] = *expected
	}
	resultJSON, e := compactJSON(result)
	if e != nil {
		return Result{}, e
	}
	receipt = operationReceipt{1, command, requestID, fingerprint, branch, "pending", path, expected, digest, resultJSON}
	encoded, e := compactJSON(receipt)
	if e != nil {
		return Result{}, e
	}
	stored, e = r.blob(ctx, string(encoded), true)
	if e != nil {
		return Result{}, e
	}
	if e = r.update(ctx, ref, stored, ""); e != nil {
		existing, actual, err := r.readOperation(ctx, ref)
		if err != nil {
			return Result{}, err
		}
		if existing == "" || actual.Operation != command || actual.RequestFingerprint != fingerprint || actual.WorktreeRef != branch {
			return Result{}, failure("idempotency-conflict", "request concurrently bound to different input or worktree")
		}
		stored, receipt = existing, actual
	}
	return r.reconcileOperation(ctx, cfg, ref, stored, receipt, desired)
}
func (r *repository) readOperation(ctx context.Context, ref string) (string, operationReceipt, error) {
	var receipt operationReceipt
	oid, e := r.ref(ctx, ref)
	if e != nil || oid == "" {
		return oid, receipt, e
	}
	objects, e := r.objects(ctx, []string{oid}, "blob")
	if e != nil {
		return oid, receipt, e
	}
	if e = decodeJSON([]byte(objects[0]), &receipt); e != nil {
		return oid, receipt, e
	}
	if receipt.Schema != 1 || (receipt.Status != "pending" && receipt.Status != "completed") || !validObject(receipt.DesiredDigest) || !normalized(receipt.Path) {
		return oid, receipt, invalid("invalid operation receipt")
	}
	return oid, receipt, nil
}
func (r *repository) authorResult(ctx context.Context, cfg Config, receipt operationReceipt, replay bool) (Result, error) {
	src := source("worktree", cfg.RepositoryID)
	outcome := "already-applied"
	if !replay {
		i, e := r.worktree(ctx)
		if e != nil {
			return Result{}, e
		}
		src = i.Source
		outcome = "applied"
	}
	data := map[string]any{"outcome": outcome, "requestId": receipt.RequestID, "requestFingerprint": receipt.RequestFingerprint, "replayed": replay, "result": receipt.Result}
	return successful(receipt.Operation, &src, data, outcome+": "+receipt.Path), nil
}
func (r *repository) reconcileOperation(ctx context.Context, cfg Config, ref, stored string, receipt operationReceipt, desired string) (Result, error) {
	if receipt.Status == "completed" {
		return r.authorResult(ctx, cfg, receipt, true)
	}
	path := filepath.Join(r.root, filepath.FromSlash(receipt.Path))
	if !normalized(receipt.Path) {
		return Result{}, invalid("unsafe receipt path")
	}
	current, e := regularFile(path)
	_, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return Result{}, invalid("record path: %v", statErr)
	}
	exists := statErr == nil
	if exists && e != nil {
		return Result{}, e
	}
	currentDigest := ""
	if exists {
		currentDigest, e = r.blob(ctx, current, false)
		if e != nil {
			return Result{}, e
		}
	}
	if currentDigest != receipt.DesiredDigest {
		if receipt.ExpectedDigest == nil && exists {
			return Result{}, failure("idempotency-conflict", "record differs from pending request result")
		}
		if receipt.ExpectedDigest != nil && (!exists || currentDigest != *receipt.ExpectedDigest) {
			return Result{}, failure("stale-input", "record changed while transition was pending")
		}
		if desired == "" {
			return Result{}, failure("stale-input", "pending operation requires original input")
		}
		digest, e := r.blob(ctx, desired, false)
		if e != nil {
			return Result{}, e
		}
		if digest != receipt.DesiredDigest {
			return Result{}, failure("idempotency-conflict", "request no longer renders its recorded result; preserve receipt")
		}
		if e = r.writeRecord(ctx, cfg, path, desired, receipt.ExpectedDigest, stored); e != nil {
			return Result{}, e
		}
	} else if receipt.ExpectedDigest != nil {
		if e = r.unlockTransition(ctx, cfg, path, stored); e != nil {
			return Result{}, e
		}
	}
	receipt.Status = "completed"
	encoded, e := compactJSON(receipt)
	if e != nil {
		return Result{}, e
	}
	completed, e := r.blob(ctx, string(encoded), true)
	if e != nil {
		return Result{}, e
	}
	if e = r.update(ctx, ref, completed, stored); e != nil {
		_, actual, err := r.readOperation(ctx, ref)
		if err != nil {
			return Result{}, err
		}
		if actual.Status != "completed" || actual.RequestFingerprint != receipt.RequestFingerprint || actual.WorktreeRef != receipt.WorktreeRef {
			return Result{}, e
		}
		receipt = actual
	}
	return r.authorResult(ctx, cfg, receipt, false)
}
