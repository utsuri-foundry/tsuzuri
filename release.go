package tsuzuri

import (
	"context"
	"errors"
)

func (r *repository) deleteRemote(ctx context.Context, s *snapshot, ref, expected string) error {
	output, pushErr := r.exec(ctx, "", nil, "-c", "push.gpgSign=false", "push", "--porcelain", "--no-signed", "--no-follow-tags", "--recurse-submodules=no", "--force-with-lease="+ref+":"+expected, s.remote, ":"+ref)
	// A failed response does not establish that the conditional write failed.
	// Always reconcile; preserve both causes if its outcome remains uncertain.
	actual, e := r.exact(ctx, s.remote, ref)
	if e != nil {
		return pushFailure(failure("indeterminate", "remote release could not be reconciled: "+e.Error()).causedBy(e), output, pushErr).details(map[string]any{"remoteRef": ref, "expectedAnchor": expected, "knownEffects": []string{}, "uncertainty": "the conditional deletion may or may not have been accepted", "safeAction": "rerun release to reconcile exact remote ref before local cleanup"})
	}
	if actual == "" {
		return nil
	}
	kind := "claim-conflict"
	if actual != expected {
		kind = "lost-ownership"
	}
	return pushFailure(failure(kind, "conditional remote release did not remove ref"), output, pushErr).details(map[string]any{"remoteRef": ref, "expectedAnchor": expected, "actualObjectId": actual, "safeAction": "inspect status and preserve local work"})
}
func partialRelease(e error, id, ref, anchor string) error {
	var app *Error
	if errors.As(e, &app) && app.Details == nil {
		app.Details = map[string]any{"taskId": id, "remoteRef": ref, "anchorObjectId": anchor, "knownEffects": []string{"remote claim ref is absent"}, "safeAction": "inspect local status and rerun release without deleting local work manually"}
	}
	return e
}

func (r *repository) requireUnusedLocalClaim(ctx context.Context, ref, anchor string) error {
	branch, e := r.ref(ctx, ref)
	if e != nil {
		return e
	}
	if branch != "" && branch != anchor {
		return failure("preserved-work", "local branch contains implementation commits; unused-claim recovery refused")
	}
	w, e := r.findWorktree(ctx, ref)
	if e != nil {
		return e
	}
	if w != nil {
		if w.locked || w.prunable || w.head != anchor {
			return failure("preserved-work", "worktree is not an unused healthy claim worktree")
		}
		return r.requireClean(ctx, w.path)
	}
	return nil
}

func (r *repository) requireAbsentClaim(ctx context.Context, s *snapshot, ref string) error {
	actual, err := r.exact(ctx, s.remote, ref)
	if err != nil {
		return failure("indeterminate", "cannot confirm claim absence before release cleanup").causedBy(err)
	}
	if actual != "" {
		return failure("preserved-work", "claim reappeared before release cleanup").details(map[string]any{
			"remoteRef": ref, "actualObjectId": actual,
			"safeAction": "stop cleanup; preserve receipts and work; inspect both remote refs with the repository owner",
		})
	}
	return nil
}
func (r *repository) release(ctx context.Context, s *snapshot, id string) (Result, error) {
	cfg := s.loaded.Config
	receipt := cfg.receipt(id)
	ref := cfg.claimRef(id)
	anchor, e := r.ref(ctx, receipt)
	if e != nil {
		return Result{}, e
	}
	if anchor == "" {
		if err := s.orphanAssignmentError(id); err != nil {
			return Result{}, err
		}
		return Result{}, failure("claim-conflict", "release requires local receipt")
	}
	m, _, e := r.anchor(ctx, s, observedRef{id, ref, anchor})
	if e != nil {
		return Result{}, e
	}
	if m.AnchorObjectID != anchor {
		return Result{}, failure("claim-conflict", "receipt must be exact anchor")
	}
	if observed, exists := s.claims[id]; exists && observed.ObjectID != anchor {
		return Result{}, failure("lost-ownership", "observed claim tip contains implementation; release refused")
	}
	ar, e := r.ref(ctx, cfg.assignmentReceipt(id))
	if e != nil {
		return Result{}, e
	}
	expectedAssignment := ""
	if obs, ok := s.assignments[id]; ok {
		a, e := r.assignment(ctx, s, obs, m)
		if e != nil {
			return Result{}, e
		}
		if ar != a.ObjectID {
			return Result{}, failure("lost-ownership", "release requires current assignment receipt")
		}
		expectedAssignment = a.ObjectID
	} else if ar != "" {
		yes, e := r.ancestor(ctx, anchor, ar)
		if e != nil {
			return Result{}, e
		}
		if !yes || ar == anchor {
			return Result{}, failure("lost-ownership", "assignment receipt unrelated to claim")
		}
	}
	if e = r.requireAssignment(ctx, s, id, expectedAssignment); e != nil {
		return Result{}, e
	}
	actual, e := r.exact(ctx, s.remote, ref)
	if e != nil {
		return Result{}, e
	}
	absent := actual == ""
	if !absent && actual != anchor {
		return Result{}, failure("lost-ownership", "remote claim no longer equals receipt anchor").details(map[string]any{"taskId": id, "remoteRef": ref, "expectedAnchor": anchor, "actualObjectId": actual, "safeAction": "inspect status and preserve local work"})
	}
	if e = r.requireUnusedLocalClaim(ctx, ref, anchor); e != nil {
		return Result{}, e
	}
	if !absent {
		if e = r.deleteRemote(ctx, s, ref, anchor); e != nil {
			return Result{}, e
		}
		actual, e = r.exact(ctx, s.remote, ref)
		if e != nil {
			return Result{}, e
		}
		if actual != "" {
			return Result{}, failure("preserved-work", "remote deletion did not persist")
		}
	}
	partial := func(e error) (Result, error) { return Result{}, partialRelease(e, id, ref, anchor) }
	removedRemoteAssignment := false
	if obs, ok := s.assignments[id]; ok {
		if e = r.requireAbsentClaim(ctx, s, ref); e != nil {
			return partial(e)
		}
		if e = r.deleteRemote(ctx, s, obs.RefName, obs.ObjectID); e != nil {
			return partial(e)
		}
		actual, e = r.exact(ctx, s.remote, obs.RefName)
		if e != nil {
			return partial(e)
		}
		if actual != "" {
			return partial(failure("preserved-work", "assignment changed after claim release"))
		}
		removedRemoteAssignment = true
	}
	if e = r.requireAbsentClaim(ctx, s, ref); e != nil {
		return partial(e)
	}
	branch, e := r.ref(ctx, ref)
	if e != nil {
		return partial(e)
	}
	if branch != "" && branch != anchor {
		return partial(failure("preserved-work", "partial-cleanup: local branch moved and was preserved"))
	}
	removedAssignment, e := r.removeIfUnchanged(ctx, cfg.assignmentReceipt(id), ar)
	if e != nil {
		return partial(e)
	}
	var removedWorktree *string
	w, e := r.findWorktree(ctx, ref)
	if e != nil {
		return partial(e)
	}
	if w != nil {
		if w.locked || w.prunable || w.head != anchor {
			return partial(failure("preserved-work", "worktree changed before removal"))
		}
		if e = r.requireClean(ctx, w.path); e != nil {
			return partial(e)
		}
		if _, e = r.run(ctx, "worktree", "remove", w.path); e != nil {
			return partial(failure("preserved-work", "partial-cleanup: worktree preserved: "+e.Error()).causedBy(e))
		}
		removedWorktree = &w.path
	}
	removedBranch, e := r.removeIfUnchanged(ctx, ref, anchor)
	if e != nil {
		return partial(e)
	}
	removedReceipt, e := r.removeIfUnchanged(ctx, receipt, anchor)
	if e != nil {
		return partial(e)
	}
	src := source("worktree", cfg.RepositoryID)
	return successful("release", &src, ReleaseResult{id, ref, anchor, removedWorktree, removedBranch, removedReceipt, removedAssignment, absent, removedRemoteAssignment}, "Released unused claim "+id), nil
}
