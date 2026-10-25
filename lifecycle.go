package tsuzuri

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Coordination describes one explicit ownership operation. It never grants
// authority to execute work; callers must retain their own authorization checks.
type Coordination struct {
	Command, ID, Owner, Worker, Worktree, ExpectedDigest, To, Checkpoint string
	NoWorktree                                                           bool
}

// Materialization identifies a local implementation branch and worktree.
type Materialization struct {
	TaskID         string `json:"taskId"`
	Branch         string `json:"branch"`
	WorktreePath   string `json:"worktreePath"`
	AnchorObjectID string `json:"anchorObjectId"`
	Resumed        bool   `json:"resumed"`
}

// AreaOverlap reports shared areas with other claimed tasks; it is advisory.
type AreaOverlap struct {
	TaskID     string   `json:"taskId"`
	Owner      *string  `json:"owner"`
	Areas      []string `json:"areas"`
	Incomplete bool     `json:"incomplete"`
	Error      *string  `json:"error"`
}

// ClaimResult binds ownership, original task revision and optional workspace.
type ClaimResult struct {
	Metadata        ClaimMetadata       `json:"metadata"`
	RemoteRef       string              `json:"remoteRef"`
	SnapshotID      string              `json:"snapshotId"`
	Overlaps        []AreaOverlap       `json:"overlaps"`
	Assignment      *AssignmentMetadata `json:"assignment"`
	ClaimedTask     TaskRevision        `json:"claimedTask"`
	Materialization *Materialization    `json:"materialization"`
}

// OrphanAssignmentDetails describes observed evidence, not repair authority.
// A surviving assignment cannot establish the missing implementation tip.
type OrphanAssignmentDetails struct {
	Issue              string  `json:"issue"`
	RepositoryID       string  `json:"repositoryId"`
	TaskID             string  `json:"taskId"`
	BaseObjectID       string  `json:"baseObjectId"`
	SnapshotID         string  `json:"snapshotId"`
	ClaimRef           string  `json:"claimRef"`
	ClaimObjectID      *string `json:"claimObjectId"`
	AssignmentRef      string  `json:"assignmentRef"`
	AssignmentObjectID string  `json:"assignmentObjectId"`
	SafeAction         string  `json:"safeAction"`
}

func (s *snapshot) orphanAssignmentError(id string) error {
	a, exists := s.assignments[id]
	if _, claimed := s.claims[id]; claimed || !exists {
		return nil
	}
	c := s.loaded.Config
	return failure("claim-conflict", "orphan assignment: assignment ref exists without a remote claim; recovery requires original evidence").details(OrphanAssignmentDetails{
		Issue: "orphan-assignment", RepositoryID: c.RepositoryID, TaskID: id,
		BaseObjectID: s.base, SnapshotID: s.id, ClaimRef: c.claimRef(id),
		AssignmentRef: a.RefName, AssignmentObjectID: a.ObjectID,
		SafeAction: "stop execution; inspect status and preserve assignment refs, original receipts and checkpoints; involve the repository owner before recovery; do not delete the assignment or create a replacement claim",
	})
}

func (e *Error) details(v any) *Error { e.Details = v; return e }

// Coordinate executes an explicit ownership command and returns its result envelope.
func (c *Client) Coordinate(ctx context.Context, request Coordination, options ...OperationOptions) (Result, error) {
	if !validID(request.ID) {
		return Result{}, failure("usage", "invalid task identifier")
	}
	if request.NoWorktree && request.Worktree != "" {
		return Result{}, failure("usage", "--no-worktree conflicts with --worktree")
	}
	if request.Worker != "" && !validIdentity(request.Worker) || request.To != "" && !validIdentity(request.To) {
		return Result{}, invalid("invalid worker identity")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	r, e := c.session(ctx, options...)
	if e != nil {
		return Result{}, e
	}
	s, e := r.snapshot(ctx)
	if e != nil {
		return Result{}, e
	}
	switch request.Command {
	case "claim":
		return r.claim(ctx, s, request)
	case "accept":
		return r.accept(ctx, s, request)
	case "handoff":
		return r.handoff(ctx, s, request)
	case "materialize":
		return r.materialize(ctx, s, request)
	case "release":
		return r.release(ctx, s, request.ID)
	default:
		return Result{}, failure("usage", "unknown coordination command")
	}
}
func (r *repository) claim(ctx context.Context, s *snapshot, q Coordination) (Result, error) {
	t, ok := s.loaded.Tasks[q.ID]
	if !ok {
		return Result{}, failure("usage", "unknown task "+q.ID)
	}
	if !s.loaded.claimable(t) {
		return Result{}, failure("unavailable", "task is not claimable on canonical base")
	}
	owner := q.Owner
	if owner == "" {
		owner = s.owner
	}
	if !validIdentity(owner) {
		return Result{}, invalid("claim requires a valid owner")
	}
	cfg := s.loaded.Config
	receipt := cfg.receipt(q.ID)
	remoteRef := cfg.claimRef(q.ID)
	old, e := r.ref(ctx, receipt)
	if e != nil {
		return Result{}, e
	}
	var m ClaimMetadata
	var revision *TaskRevision
	if old != "" {
		m, revision, e = r.anchor(ctx, s, observedRef{q.ID, remoteRef, old})
		if e != nil {
			return Result{}, e
		}
		if m.AnchorObjectID != old || m.Owner != owner {
			return Result{}, failure("claim-conflict", "local receipt does not match this owner and anchor")
		}
	} else {
		if e = s.orphanAssignmentError(q.ID); e != nil {
			return Result{}, e
		}
		if _, ok := s.claims[q.ID]; ok {
			return Result{}, failure("claim-conflict", "task already has a remote claim")
		}
		m, e = r.newClaim(ctx, s, q.ID, owner)
		if e != nil {
			return Result{}, e
		}
		revision = &TaskRevision{s.base, t.Path, s.loaded.Digests[t.Path]}
	}
	if q.ExpectedDigest != "" && q.ExpectedDigest != revision.RecordDigest {
		return Result{}, failure("stale-input", "task record digest differs from authorized revision").details(map[string]any{"taskId": q.ID, "expectedDigest": q.ExpectedDigest, "actualDigest": revision.RecordDigest, "baseObjectId": revision.BaseObjectID, "safeAction": "inspect the original task revision and obtain authorization"})
	}
	if observed, exists := s.claims[q.ID]; exists && observed.ObjectID != m.AnchorObjectID {
		return Result{}, failure("lost-ownership", "observed claim tip advanced beyond the original anchor; preserve implementation and reconcile")
	}
	var existingAssignment *AssignmentMetadata
	if observed, exists := s.assignments[q.ID]; exists {
		// Omitting --worker must not turn an old claim receipt into authority
		// after a handoff. Explicit initial-worker recovery remains supported.
		a, err := r.assignment(ctx, s, observed, m)
		if err != nil {
			return Result{}, err
		}
		local, err := r.ref(ctx, cfg.assignmentReceipt(q.ID))
		if err != nil {
			return Result{}, err
		}
		if a.State != "assigned" || (q.Worker != "" && (a.Worker == nil || *a.Worker != q.Worker)) {
			return Result{}, failure("lost-ownership", "claim retry cannot bypass the current worker assignment; inspect status and use accept")
		}
		initialRecovery := q.Worker != "" && local == "" && a.PreviousObjectID == m.AnchorObjectID
		if local != a.ObjectID && !initialRecovery {
			return Result{}, failure("lost-ownership", "claim retry requires the current assignment receipt; use accept with the authorized worker")
		}
		existingAssignment = &a
	}
	if _, claimed := s.claims[q.ID]; !claimed && existingAssignment != nil {
		// A receipt-backed retry may restore the original unused anchor, but
		// must not hide implementation already visible in this clone.
		if e = r.requireUnusedLocalClaim(ctx, remoteRef, m.AnchorObjectID); e != nil {
			return Result{}, e
		}
	}
	observedAssignment := ""
	if existingAssignment != nil {
		observedAssignment = existingAssignment.ObjectID
	}
	if e = r.requireAssignment(ctx, s, q.ID, observedAssignment); e != nil {
		return Result{}, e
	}
	if old == "" {
		if e = r.createReceipt(ctx, receipt, m.AnchorObjectID); e != nil {
			return Result{}, e
		}
	}
	if e = r.publish(ctx, s, remoteRef, "", m.AnchorObjectID, true); e != nil {
		var app *Error
		if errors.As(e, &app) && app.Kind == "indeterminate" {
			app.Details = map[string]any{"taskId": q.ID, "claimId": m.ClaimID, "remoteRef": remoteRef, "anchorObjectId": m.AnchorObjectID, "knownEffects": []string{"local receipt exists"}, "uncertainty": "remote ownership was not confirmed", "safeAction": "rerun claim with the same owner", "request": map[string]any{"worker": nullable(q.Worker), "noWorktree": q.NoWorktree, "expectedDigest": nullable(q.ExpectedDigest)}}
		}
		return Result{}, e
	}
	result := ClaimResult{Metadata: m, RemoteRef: remoteRef, SnapshotID: s.id, Overlaps: r.overlaps(ctx, s, q.ID), ClaimedTask: *revision}
	result.Assignment = existingAssignment
	if q.Worker != "" && result.Assignment == nil {
		a, e := r.initialAssignment(ctx, s, m, q.Worker)
		if e != nil {
			return Result{}, e
		}
		result.Assignment = &a
	}
	actual, e := r.exact(ctx, s.remote, remoteRef)
	if e != nil {
		return Result{}, failure("indeterminate", "cannot reconcile claim before local effects").causedBy(e)
	}
	if actual != m.AnchorObjectID {
		return Result{}, failure("lost-ownership", "claim tip changed before local effects")
	}
	expectedAssignment := ""
	if result.Assignment != nil {
		expectedAssignment = result.Assignment.ObjectID
	}
	if e = r.requireAssignment(ctx, s, q.ID, expectedAssignment); e != nil {
		return Result{}, e
	}
	if result.Assignment != nil {
		if _, e = r.assignmentReceipt(ctx, cfg, q.ID, m.AnchorObjectID, result.Assignment.ObjectID); e != nil {
			return Result{}, e
		}
	}
	if !q.NoWorktree {
		v, e := r.materializeConfirmed(ctx, s, m, m.AnchorObjectID, q.Worktree)
		if e != nil {
			return Result{}, materializationError(e, m, remoteRef, "")
		}
		result.Materialization = &v
	}
	src := source("remote-snapshot", m.RepositoryID)
	src.BaseObjectID = &m.BaseObjectID
	src.SnapshotID = &s.id
	src.RefreshResult = stringPtr("success")
	return successful("claim", &src, result, fmt.Sprintf("Claimed %s as %s", q.ID, m.ClaimID)), nil
}
func (r *repository) initialAssignment(ctx context.Context, s *snapshot, m ClaimMetadata, worker string) (AssignmentMetadata, error) {
	cfg := s.loaded.Config
	if o, ok := s.assignments[m.TaskID]; ok {
		a, e := r.assignment(ctx, s, o, m)
		if e != nil {
			return a, e
		}
		if a.State != "assigned" || a.Worker == nil || *a.Worker != worker {
			return a, failure("claim-conflict", "task already has a different worker assignment")
		}
		receipt, e := r.ref(ctx, cfg.assignmentReceipt(m.TaskID))
		if e != nil {
			return a, e
		}
		if receipt == a.ObjectID || receipt == "" && a.PreviousObjectID == m.AnchorObjectID {
			return a, nil
		}
		return a, failure("lost-ownership", "assignment has moved; use accept")
	}
	a, e := r.newAssignment(ctx, m, m.AnchorObjectID, &worker)
	if e != nil {
		return a, e
	}
	return a, r.publish(ctx, s, cfg.assignmentRef(m.TaskID), "", a.ObjectID, false)
}

// publish always reconciles uncertain pushes. An exact observed object is an
// observation, not an atomic fence across claim and assignment refs.
func (r *repository) publish(ctx context.Context, s *snapshot, ref, expected, desired string, claim bool) error {
	actual, e := r.exact(ctx, s.remote, ref)
	if e != nil {
		return e
	}
	if actual == desired {
		return nil
	}
	mismatch := func(actual string) *Error {
		kind := "claim-conflict"
		if claim {
			kind = "lost-ownership"
		}
		return failure(kind, "remote ownership changed").details(map[string]any{"remoteRef": ref, "expectedAnchor": desired, "actualObjectId": nullable(actual), "safeAction": "inspect status and preserve local receipts"})
	}
	if actual != expected {
		return mismatch(actual)
	}
	output, pushErr := r.exec(ctx, "", nil, "-c", "push.gpgSign=false", "-c", "push.followTags=false", "push", "--porcelain", "--no-signed", "--no-follow-tags", "--recurse-submodules=no", "--force-with-lease="+ref+":"+expected, s.remote, desired+":"+ref)
	actual, e = r.exact(ctx, s.remote, ref)
	if e != nil {
		return pushFailure(failure("indeterminate", "could not reconcile remote write: "+e.Error()).causedBy(e), output, pushErr)
	}
	if actual == desired {
		return nil
	}
	if actual != "" {
		result := mismatch(actual)
		if actual == expected {
			result.Message = "conditional remote write was not acquired"
		}
		return pushFailure(result, output, pushErr)
	}
	if pushErr == nil && output.code == 0 {
		return failure("indeterminate", "remote write reported success but ref is absent")
	}
	return pushFailure(failure("claim-conflict", "conditional remote write was not acquired"), output, pushErr)
}
func (r *repository) createReceipt(ctx context.Context, ref, object string) error {
	if e := r.update(ctx, ref, object, ""); e != nil {
		return failure("claim-conflict", "could not create local receipt: "+e.Error()).causedBy(e)
	}
	return nil
}
func (r *repository) ancestor(ctx context.Context, before, after string) (bool, error) {
	o, e := r.exec(ctx, "", nil, "merge-base", "--is-ancestor", before, after)
	if e != nil {
		return false, e
	}
	if o.code > 1 {
		return false, invalid("cannot verify ancestry: %s", o.diagnostic)
	}
	return o.code == 0, nil
}
func (r *repository) assignmentReceipt(ctx context.Context, c Config, id, anchor, object string) (bool, error) {
	ref := c.assignmentReceipt(id)
	old, e := r.ref(ctx, ref)
	if e != nil {
		return false, e
	}
	if old == object {
		return false, nil
	}
	if old != "" {
		a, e := r.ancestor(ctx, anchor, old)
		if e != nil {
			return false, e
		}
		b, e := r.ancestor(ctx, old, object)
		if e != nil {
			return false, e
		}
		if !a || !b {
			return false, failure("lost-ownership", "local assignment receipt is unrelated")
		}
	}
	if e = r.update(ctx, ref, object, old); e != nil {
		return false, failure("lost-ownership", "assignment receipt changed concurrently")
	}
	return true, nil
}
func (r *repository) active(ctx context.Context, s *snapshot, id string) (observedRef, ClaimMetadata, *TaskRevision, *AssignmentMetadata, error) {
	var o observedRef
	var m ClaimMetadata
	t, ok := s.loaded.Tasks[id]
	if !ok {
		return o, m, nil, nil, failure("usage", "unknown task "+id)
	}
	if terminal(t.State) {
		return o, m, nil, nil, failure("unavailable", "terminal task cannot be handed off")
	}
	o, ok = s.claims[id]
	if !ok {
		if err := s.orphanAssignmentError(id); err != nil {
			return o, m, nil, nil, err
		}
		return o, m, nil, nil, failure("claim-conflict", "task has no active remote claim")
	}
	m, rev, e := r.anchor(ctx, s, o)
	if e != nil {
		return o, m, nil, nil, e
	}
	var a *AssignmentMetadata
	if obs, ok := s.assignments[id]; ok {
		v, err := r.assignment(ctx, s, obs, m)
		if err != nil {
			return o, m, rev, nil, err
		}
		a = &v
	}
	return o, m, rev, a, nil
}
func (r *repository) overlaps(ctx context.Context, s *snapshot, id string) []AreaOverlap {
	result := []AreaOverlap{}
	candidate := s.loaded.Tasks[id]
	for _, other := range sortedKeys(s.claims) {
		if other == id {
			continue
		}
		m, _, e := r.anchor(ctx, s, s.claims[other])
		if e != nil {
			result = append(result, AreaOverlap{TaskID: other, Areas: []string{}, Incomplete: true, Error: stringPtr(e.Error())})
			continue
		}
		t, ok := s.loaded.Tasks[other]
		if !ok || terminal(t.State) {
			continue
		}
		areas := []string{}
		for _, a := range candidate.Areas {
			if slices.Contains(t.Areas, a) {
				areas = append(areas, a)
			}
		}
		if len(areas) > 0 {
			result = append(result, AreaOverlap{TaskID: other, Owner: &m.Owner, Areas: areas})
		}
	}
	return result
}
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func materializationError(err error, m ClaimMetadata, ref, worker string) error {
	var app *Error
	if !errors.As(err, &app) || app.Kind != "local-materialization" || app.Details != nil {
		return err
	}
	effects := []string{"remote claim is confirmed"}
	action := "resolve local conflict and run materialize " + m.TaskID
	if worker != "" {
		effects = []string{"worker assignment is confirmed", "local receipt exists"}
		action = "resolve local conflict and rerun accept with the same worker"
	}
	app.Details = map[string]any{"taskId": m.TaskID, "remoteRef": ref, "anchorObjectId": m.AnchorObjectID, "worker": nullable(worker), "knownEffects": effects, "safeAction": action}
	return err
}
