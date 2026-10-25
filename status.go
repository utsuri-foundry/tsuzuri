package tsuzuri

import (
	"context"
	"fmt"
	"strings"
)

type LocalClaimObservation struct {
	TaskID                    string   `json:"taskId"`
	ReceiptObjectID           *string  `json:"receiptObjectId"`
	AssignmentReceiptObjectID *string  `json:"assignmentReceiptObjectId"`
	BranchObjectID            *string  `json:"branchObjectId"`
	WorktreePath              *string  `json:"worktreePath"`
	WorktreeHead              *string  `json:"worktreeHead"`
	WorktreeClean             *bool    `json:"worktreeClean"`
	Issues                    []string `json:"issues"`
}

func (r *repository) localClaims(ctx context.Context, i *inventory) ([]LocalClaimObservation, error) {
	cfg := i.Config
	prefixes := []string{"refs/tsuzuri/receipts/" + cfg.RepositoryID + "/", "refs/tsuzuri/assignment-receipts/" + cfg.RepositoryID + "/", "refs/heads/" + cfg.ClaimNamespace + "/"}
	all := map[string]*LocalClaimObservation{}
	ensure := func(id string) *LocalClaimObservation {
		if all[id] == nil {
			all[id] = &LocalClaimObservation{TaskID: id, Issues: []string{}}
		}
		return all[id]
	}
	for n, prefix := range prefixes {
		text, e := r.run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
		if e != nil {
			return nil, e
		}
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if line == "" {
				continue
			}
			f := strings.Fields(line)
			if len(f) != 2 || !strings.HasPrefix(f[0], prefix) || !validObject(f[1]) {
				return nil, invalid("malformed local ref")
			}
			v := ensure(strings.TrimPrefix(f[0], prefix))
			switch n {
			case 0:
				v.ReceiptObjectID = stringPtr(f[1])
			case 1:
				v.AssignmentReceiptObjectID = stringPtr(f[1])
			case 2:
				v.BranchObjectID = stringPtr(f[1])
			}
		}
	}
	trees, e := r.worktrees(ctx)
	if e != nil {
		return nil, e
	}
	for _, w := range trees {
		if strings.HasPrefix(w.branch, prefixes[2]) {
			v := ensure(strings.TrimPrefix(w.branch, prefixes[2]))
			v.WorktreePath = stringPtr(w.path)
			v.WorktreeHead = stringPtr(w.head)
			clean, e := r.clean(ctx, w.path)
			if e != nil {
				return nil, e
			}
			v.WorktreeClean = &clean
			if v.BranchObjectID != nil {
				if *v.BranchObjectID != w.head {
					v.Issues = append(v.Issues, "worktree HEAD and local branch disagree")
				}
				if w.locked {
					v.Issues = append(v.Issues, "worktree is locked")
				}
				if w.prunable {
					v.Issues = append(v.Issues, "worktree is prunable")
				}
			}
		}
	}
	out := []LocalClaimObservation{}
	for _, id := range sortedKeys(all) {
		v := all[id]
		if _, ok := i.Tasks[id]; !ok {
			v.Issues = append(v.Issues, "task does not exist in the canonical inventory")
		}
		if v.ReceiptObjectID != nil && v.BranchObjectID != nil {
			yes, e := r.ancestor(ctx, *v.ReceiptObjectID, *v.BranchObjectID)
			if e != nil {
				return nil, e
			}
			if !yes {
				v.Issues = append(v.Issues, "local branch does not descend from receipt anchor")
			}
		}
		out = append(out, *v)
	}
	return out, nil
}
func snapshotData(s *snapshot) map[string]any {
	claims := []map[string]string{}
	assignments := []map[string]string{}
	for _, id := range sortedKeys(s.claims) {
		o := s.claims[id]
		claims = append(claims, map[string]string{"ref": o.RefName, "objectId": o.ObjectID})
	}
	for _, id := range sortedKeys(s.assignments) {
		o := s.assignments[id]
		assignments = append(assignments, map[string]string{"ref": o.RefName, "objectId": o.ObjectID})
	}
	return map[string]any{"id": s.id, "baseObjectId": s.base, "baseRef": s.baseRef, "claimPrefix": s.claimPrefix, "assignmentPrefix": s.assignmentPrefix, "observedClaimObjectIds": claims, "observedAssignmentObjectIds": assignments, "refreshResult": "success"}
}
func orphanAssignments(s *snapshot) []AssignmentObservation {
	out := []AssignmentObservation{}
	for _, id := range sortedKeys(s.assignments) {
		if _, ok := s.claims[id]; !ok {
			o := s.assignments[id]
			out = append(out, AssignmentObservation{TaskID: id, RefName: o.RefName, ObjectID: o.ObjectID, Status: "orphan", Error: stringPtr("assignment has no remote claim")})
		}
	}
	return out
}
func (r *repository) status(ctx context.Context, q Query) (Result, error) {
	s, e := r.snapshot(ctx)
	if e != nil {
		return Result{}, e
	}
	if q.ID != "" {
		if _, ok := s.loaded.Tasks[q.ID]; !ok {
			return Result{}, failure("usage", "unknown task "+q.ID)
		}
	}
	locals, e := r.localClaims(ctx, s.loaded)
	if e != nil {
		return Result{}, e
	}
	local := []LocalClaimObservation{}
	claims := []ClaimObservation{}
	orphans := []AssignmentObservation{}
	invalidClaims, invalidAssignments := 0, 0
	for _, v := range locals {
		if q.ID == "" || v.TaskID == q.ID {
			local = append(local, v)
		}
	}
	lines := []string{}
	for _, c := range r.observe(ctx, s) {
		if q.ID == "" || c.TaskID == q.ID {
			claims = append(claims, c)
			if !c.Valid {
				invalidClaims++
			}
			if c.Assignment != nil && !c.Assignment.Valid {
				invalidAssignments++
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", c.Status, c.TaskID, c.ObjectID))
		}
	}
	for _, a := range orphanAssignments(s) {
		if q.ID == "" || a.TaskID == q.ID {
			orphans = append(orphans, a)
			invalidAssignments++
			lines = append(lines, "orphan-assignment\t"+a.TaskID+"\t"+a.ObjectID)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No observed claims.")
	}
	return successful(q.Command, &s.loaded.Source, map[string]any{"snapshot": snapshotData(s), "claims": claims, "local": local, "invalidClaims": invalidClaims, "orphanAssignments": orphans, "invalidAssignments": invalidAssignments}, strings.Join(lines, "\n")), nil
}
