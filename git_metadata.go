package tsuzuri

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (r *repository) commitMetadata(ctx context.Context, parent, message, timestamp string) (string, error) {
	tree, e := r.oid(ctx, parent+"^{tree}")
	if e != nil {
		return "", e
	}
	env := []string{"GIT_AUTHOR_NAME=Tsuzuri", "GIT_AUTHOR_EMAIL=tsuzuri@invalid", "GIT_AUTHOR_DATE=" + timestamp, "GIT_COMMITTER_NAME=Tsuzuri", "GIT_COMMITTER_EMAIL=tsuzuri@invalid", "GIT_COMMITTER_DATE=" + timestamp}
	s, e := r.input(ctx, message, env, "-c", "commit.gpgSign=false", "commit-tree", tree, "-p", parent)
	if e != nil {
		return "", e
	}
	s = strings.TrimSpace(s)
	if !validObject(s) {
		return "", invalid("invalid metadata commit ID")
	}
	return s, nil
}
func (r *repository) newClaim(ctx context.Context, s *snapshot, id, owner string) (ClaimMetadata, error) {
	m := ClaimMetadata{Version: 1, RepositoryID: s.loaded.Config.RepositoryID, TaskID: id, ClaimID: uuid(), Owner: owner, BaseObjectID: s.base, ClaimedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z")}
	if !validIdentity(owner) {
		return m, invalid("invalid owner")
	}
	msg := fmt.Sprintf("tsuzuri claim %s\n\nTsuzuri-Claim-Version: 1\nTsuzuri-Repository: %s\nTsuzuri-Task: %s\nTsuzuri-Claim: %s\nTsuzuri-Owner: %s\nTsuzuri-Base: %s\nTsuzuri-Claimed-At: %s\n", id, m.RepositoryID, id, m.ClaimID, owner, m.BaseObjectID, m.ClaimedAt)
	anchor, e := r.commitMetadata(ctx, m.BaseObjectID, msg, m.ClaimedAt)
	m.AnchorObjectID = anchor
	return m, e
}
func (r *repository) newAssignment(ctx context.Context, claim ClaimMetadata, previous string, worker *string) (AssignmentMetadata, error) {
	a := AssignmentMetadata{Version: 1, RepositoryID: claim.RepositoryID, TaskID: claim.TaskID, ClaimID: claim.ClaimID, AssignmentID: uuid(), State: "handoff-pending", Worker: worker, PreviousObjectID: previous, UpdatedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z")}
	workerLine := ""
	if worker != nil {
		if !validIdentity(*worker) {
			return a, invalid("invalid worker")
		}
		a.State = "assigned"
		workerLine = "Tsuzuri-Worker: " + *worker + "\n"
	}
	msg := fmt.Sprintf("tsuzuri assignment %s\n\nTsuzuri-Assignment-Version: 1\nTsuzuri-Repository: %s\nTsuzuri-Task: %s\nTsuzuri-Claim: %s\nTsuzuri-Assignment: %s\nTsuzuri-State: %s\n%sTsuzuri-Previous: %s\nTsuzuri-Updated-At: %s\n", a.TaskID, a.RepositoryID, a.TaskID, a.ClaimID, a.AssignmentID, a.State, workerLine, previous, a.UpdatedAt)
	object, e := r.commitMetadata(ctx, previous, msg, a.UpdatedAt)
	a.ObjectID = object
	return a, e
}
func (r *repository) exact(ctx context.Context, remote, ref string) (string, error) {
	o, e := r.exec(ctx, "", nil, "ls-remote", "--refs", remote, ref)
	if e != nil {
		return "", failure("remote", e.Error()).causedBy(e)
	}
	if o.code != 0 {
		return "", failure("remote", o.diagnostic)
	}
	var found string
	for _, line := range strings.Split(strings.TrimSpace(o.text), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || f[1] != ref || !validObject(f[0]) || found != "" {
			return "", failure("remote", "malformed exact remote ref result")
		}
		found = f[0]
	}
	return found, nil
}
