package tsuzuri

import "context"

// Acceptance is fresh reconciliation, not a guarantee that an already-running
// process can be fenced. Consumers must verify both refs before acting.
func (r *repository) accept(ctx context.Context, s *snapshot, q Coordination) (Result, error) {
	if !validIdentity(q.Worker) {
		return Result{}, invalid("accept requires valid worker")
	}
	o, m, rev, a, e := r.active(ctx, s, q.ID)
	if e != nil {
		return Result{}, e
	}
	if a == nil {
		return Result{}, failure("claim-conflict", "task has no assignment to accept")
	}
	cfg := s.loaded.Config
	if a.State == "handoff-pending" {
		next, e := r.newAssignment(ctx, m, a.ObjectID, &q.Worker)
		if e != nil {
			return Result{}, e
		}
		if e = r.publish(ctx, s, cfg.assignmentRef(q.ID), a.ObjectID, next.ObjectID, false); e != nil {
			return Result{}, e
		}
		a = &next
	} else if a.Worker == nil || *a.Worker != q.Worker {
		return Result{}, failure("claim-conflict", "task is assigned to a different worker")
	}
	actual, e := r.exact(ctx, s.remote, cfg.claimRef(q.ID))
	if e != nil {
		return Result{}, e
	}
	if actual != o.ObjectID {
		return Result{}, failure("indeterminate", "claim changed during acceptance; retry")
	}
	details := map[string]any{"repositoryId": cfg.RepositoryID, "baseObjectId": s.base, "snapshotId": s.id, "taskId": q.ID, "claimId": m.ClaimID, "remoteRef": cfg.assignmentRef(q.ID), "expectedObjectId": a.ObjectID, "receiptWritesStarted": false, "safeAction": "inspect status and preserve existing receipts; do not execute under an unconfirmed assignment"}
	actual, e = r.exact(ctx, s.remote, cfg.assignmentRef(q.ID))
	if e != nil {
		return Result{}, failure("indeterminate", "could not reconcile assignment before incoming receipts: "+e.Error()).causedBy(e).details(details)
	}
	if actual != a.ObjectID {
		details["actualObjectId"] = nullable(actual)
		return Result{}, failure("lost-ownership", "assignment changed before incoming receipts").details(details)
	}
	updated, e := r.assignmentReceipt(ctx, cfg, q.ID, m.AnchorObjectID, a.ObjectID)
	if e != nil {
		return Result{}, e
	}
	receipt, e := r.ref(ctx, cfg.receipt(q.ID))
	if e != nil {
		return Result{}, e
	}
	if receipt != "" && receipt != m.AnchorObjectID {
		return Result{}, failure("claim-conflict", "local receipt points to unrelated claim")
	}
	created := receipt == ""
	if created {
		if e = r.createReceipt(ctx, cfg.receipt(q.ID), m.AnchorObjectID); e != nil {
			return Result{}, e
		}
	}
	var materialization *Materialization
	if !q.NoWorktree {
		v, e := r.materializeConfirmed(ctx, s, m, o.ObjectID, q.Worktree)
		if e != nil {
			return Result{}, materializationError(e, m, cfg.claimRef(q.ID), q.Worker)
		}
		materialization = &v
	}
	data := AcceptanceResult{sourceData(s), q.ID, m.ClaimID, a, cfg.assignmentRef(q.ID), created, updated, rev, o.ObjectID, materialization}
	return successful("accept", &s.loaded.Source, data, "Accepted "+q.ID+" for "+q.Worker), nil
}
func sourceData(s *snapshot) CoordinationSource {
	src := s.loaded.Source
	return CoordinationSource{src.RepositoryID, s.base, src.ConfigurationObjectID, s.id, src.ObservedClaimObjectIDs, src.ObservedAssignmentObjectIDs}
}
func matchesTarget(a *AssignmentMetadata, worker string) bool {
	return a != nil && (worker == "" && a.State == "handoff-pending" || worker != "" && a.State == "assigned" && a.Worker != nil && *a.Worker == worker)
}
func (r *repository) handoff(ctx context.Context, s *snapshot, q Coordination) (Result, error) {
	o, m, _, a, e := r.active(ctx, s, q.ID)
	if e != nil {
		return Result{}, e
	}
	cfg := s.loaded.Config
	receipt, e := r.ref(ctx, cfg.receipt(q.ID))
	if e != nil {
		return Result{}, e
	}
	if receipt == "" {
		return Result{}, failure("claim-conflict", "handoff requires local receipt")
	}
	if receipt != m.AnchorObjectID {
		return Result{}, failure("lost-ownership", "receipt differs from active claim anchor")
	}
	ar, e := r.ref(ctx, cfg.assignmentReceipt(q.ID))
	if e != nil {
		return Result{}, e
	}
	if a == nil && ar != "" {
		return Result{}, failure("claim-conflict", "local assignment receipt without remote assignment")
	}
	if a != nil && ar != a.ObjectID && !(matchesTarget(a, q.To) && (ar == "" || ar == a.PreviousObjectID)) {
		return Result{}, failure("lost-ownership", "handoff requires current assignment receipt")
	}
	if e = r.checkpoint(ctx, s, o, q.Checkpoint); e != nil {
		return Result{}, e
	}
	if !matchesTarget(a, q.To) {
		expected := ""
		previous := m.AnchorObjectID
		if a != nil {
			expected = a.ObjectID
			previous = expected
		}
		next, e := r.newAssignment(ctx, m, previous, nullable(q.To))
		if e != nil {
			return Result{}, e
		}
		if e = r.publish(ctx, s, cfg.assignmentRef(q.ID), expected, next.ObjectID, false); e != nil {
			return Result{}, e
		}
		a = &next
	}
	if e = r.requireAssignment(ctx, s, q.ID, a.ObjectID); e != nil {
		return Result{}, e
	}
	removedAssignment, e := r.removeIfUnchanged(ctx, cfg.assignmentReceipt(q.ID), ar)
	if e != nil {
		return Result{}, e
	}
	removedReceipt, e := r.removeIfUnchanged(ctx, cfg.receipt(q.ID), m.AnchorObjectID)
	if e != nil {
		return Result{}, e
	}
	data := HandoffResult{sourceData(s), q.ID, m.ClaimID, a, cfg.assignmentRef(q.ID), removedReceipt, removedAssignment}
	return successful("handoff", &s.loaded.Source, data, "Handed off "+q.ID), nil
}
func (r *repository) requireAssignment(ctx context.Context, s *snapshot, id, expected string) error {
	ref := s.loaded.Config.assignmentRef(id)
	actual, e := r.exact(ctx, s.remote, ref)
	details := map[string]any{"taskId": id, "remoteRef": ref, "expectedObjectId": nullable(expected), "safeAction": "preserve receipts and inspect status; do not act under an unconfirmed assignment"}
	if e != nil {
		return failure("indeterminate", "could not reconcile assignment before local effects").causedBy(e).details(details)
	}
	if actual != expected {
		details["actualObjectId"] = nullable(actual)
		return failure("lost-ownership", "assignment changed before local effects").details(details)
	}
	return nil
}
func (r *repository) removeIfUnchanged(ctx context.Context, ref, expected string) (bool, error) {
	if expected == "" {
		return false, nil
	}
	actual, e := r.ref(ctx, ref)
	if e != nil {
		return false, e
	}
	if actual == "" {
		return false, nil
	}
	if actual != expected {
		return false, failure("preserved-work", "local ref moved and was preserved: "+ref)
	}
	if e = r.remove(ctx, ref, expected); e != nil {
		return false, failure("preserved-work", "local ref changed during cleanup").causedBy(e)
	}
	actual, e = r.ref(ctx, ref)
	if e != nil {
		return false, e
	}
	if actual != "" {
		return false, failure("preserved-work", "local ref recreated during cleanup")
	}
	return true, nil
}
