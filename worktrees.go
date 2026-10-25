package tsuzuri

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type worktreeEntry struct {
	path, head, branch string
	locked, prunable   bool
}

func (r *repository) worktrees(ctx context.Context) ([]worktreeEntry, error) {
	text, e := r.run(ctx, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return nil, e
	}
	entries := []worktreeEntry{}
	var w worktreeEntry
	for _, field := range strings.Split(text, "\x00") {
		switch {
		case field == "":
			if w.path != "" && w.head != "" {
				entries = append(entries, w)
			}
			w = worktreeEntry{}
		case strings.HasPrefix(field, "worktree "):
			w.path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
			w.head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			w.branch = strings.TrimPrefix(field, "branch ")
		case strings.HasPrefix(field, "locked"):
			w.locked = true
		case strings.HasPrefix(field, "prunable"):
			w.prunable = true
		}
	}
	return entries, nil
}
func (r *repository) findWorktree(ctx context.Context, branch string) (*worktreeEntry, error) {
	entries, e := r.worktrees(ctx)
	if e != nil {
		return nil, e
	}
	for _, w := range entries {
		if w.branch == branch {
			return &w, nil
		}
	}
	return nil, nil
}
func (r *repository) clean(ctx context.Context, path string) (bool, error) {
	text, e := r.run(ctx, "-C", path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		return false, failure("preserved-work", "could not inspect worktree cleanliness: "+e.Error()).causedBy(e)
	}
	return text == "", nil
}
func (r *repository) requireClean(ctx context.Context, path string) error {
	clean, e := r.clean(ctx, path)
	if e != nil {
		return e
	}
	if !clean {
		return failure("preserved-work", "worktree has tracked, untracked, staged, conflicted, or submodule changes")
	}
	return nil
}
func (r *repository) destination(ctx context.Context, cfg Config, id, override string) (string, error) {
	root := cfg.WorktreeRoot
	if !filepath.IsAbs(root) {
		canonical, e := r.canonicalRoot(ctx)
		if e != nil {
			return "", e
		}
		root = filepath.Join(canonical, root)
	}
	root, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", invalid("cannot resolve worktree root: %v", e).causedBy(e)
	}
	path := override
	if path == "" {
		path = strings.ReplaceAll(cfg.WorktreeTemplate, "{task_id}", id)
	}
	if !filepath.IsAbs(path) {
		if path != filepath.Clean(path) || path == "." || path == ".." || strings.HasPrefix(path, "../") {
			return "", invalid("worktree must be normalized child path")
		}
		path = filepath.Join(root, path)
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return "", invalid("cannot resolve worktree destination parent: %v", e).causedBy(e)
	}
	if parent != root || filepath.Base(path) == "." || filepath.Base(path) == ".." {
		return "", invalid("worktree must be a direct child of configured root")
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
func (r *repository) materializeConfirmed(ctx context.Context, s *snapshot, m ClaimMetadata, tip, override string) (Materialization, error) {
	var result Materialization
	i, e := r.tree(ctx, m.BaseObjectID, true, true)
	if e != nil {
		return result, e
	}
	cfg := i.Config
	if cfg.RepositoryID != m.RepositoryID {
		return result, failure("claim-conflict", "claimed base identity differs")
	}
	branch := cfg.ClaimNamespace + "/" + m.TaskID
	ref := "refs/heads/" + branch
	path, e := r.destination(ctx, cfg, m.TaskID, override)
	if e != nil {
		return result, e
	}
	result = Materialization{TaskID: m.TaskID, Branch: branch, WorktreePath: path, AnchorObjectID: m.AnchorObjectID}
	w, e := r.findWorktree(ctx, ref)
	if e != nil {
		return result, e
	}
	local, e := r.ref(ctx, ref)
	if e != nil {
		return result, e
	}
	if w != nil {
		if w.path != path || w.locked || w.prunable || local == "" || local != w.head || local != tip {
			return result, failure("local-materialization", "existing worktree is not at the confirmed remote tip")
		}
		yes, e := r.ancestor(ctx, m.AnchorObjectID, local)
		if e != nil {
			return result, e
		}
		if !yes {
			return result, failure("local-materialization", "local branch no longer descends from claim anchor")
		}
		result.Resumed = true
		return result, nil
	}
	if _, e = os.Lstat(path); e == nil {
		return result, failure("local-materialization", "worktree destination already exists")
	} else if !os.IsNotExist(e) {
		return result, failure("local-materialization", e.Error()).causedBy(e)
	}
	args := []string{"worktree", "add"}
	if local == "" {
		args = append(args, "-b", branch, path, tip)
	} else if local == tip {
		args = append(args, path, branch)
	} else {
		return result, failure("local-materialization", "local branch differs from confirmed remote tip")
	}
	if _, e = r.run(ctx, args...); e != nil {
		return result, failure("local-materialization", "claim confirmed but materialization failed: "+e.Error()).causedBy(e)
	}
	for _, pair := range [][2]string{{"branch." + branch + ".remote", s.remote}, {"branch." + branch + ".merge", cfg.claimRef(m.TaskID)}} {
		if _, e = r.run(ctx, "config", "--local", pair[0], pair[1]); e != nil {
			return result, e
		}
	}
	return result, nil
}
func (r *repository) checkpoint(ctx context.Context, s *snapshot, o observedRef, checkpoint string) error {
	ref := s.loaded.Config.claimRef(o.TaskID)
	branch, e := r.ref(ctx, ref)
	if e != nil {
		return e
	}
	w, e := r.findWorktree(ctx, ref)
	if e != nil {
		return e
	}
	if checkpoint != "" && checkpoint != o.ObjectID {
		return failure("stale-input", "handoff checkpoint differs from remote claim tip").details(map[string]any{"expectedObjectId": checkpoint, "actualObjectId": o.ObjectID})
	}
	if !(checkpoint != "" && branch == "" && w == nil) {
		if branch == "" || branch != o.ObjectID || w == nil || w.locked || w.prunable || w.head != branch {
			return failure("preserved-work", "handoff requires a healthy materialized worktree at the exact published tip")
		}
		if e = r.requireClean(ctx, w.path); e != nil {
			return e
		}
	}
	actual, e := r.exact(ctx, s.remote, ref)
	if e != nil {
		return e
	}
	if actual != o.ObjectID {
		return failure("indeterminate", "remote claim changed during handoff preparation")
	}
	return nil
}
func (r *repository) materialize(ctx context.Context, s *snapshot, q Coordination) (Result, error) {
	cfg := s.loaded.Config
	receipt, e := r.ref(ctx, cfg.receipt(q.ID))
	if e != nil {
		return Result{}, e
	}
	if receipt == "" {
		if err := s.orphanAssignmentError(q.ID); err != nil {
			return Result{}, err
		}
		return Result{}, failure("claim-conflict", "materialize requires local receipt")
	}
	o, m, _, a, e := r.active(ctx, s, q.ID)
	if e != nil {
		return Result{}, e
	}
	if receipt != m.AnchorObjectID {
		return Result{}, failure("lost-ownership", "local receipt does not match remote anchor")
	}
	if a != nil {
		if a.State != "assigned" {
			return Result{}, failure("lost-ownership", "pending handoff cannot be materialized by the outgoing worker")
		}
		old, e := r.ref(ctx, cfg.assignmentReceipt(q.ID))
		if e != nil {
			return Result{}, e
		}
		if old != a.ObjectID {
			return Result{}, failure("lost-ownership", "materialize requires current assignment receipt")
		}
	}
	actual, e := r.exact(ctx, s.remote, cfg.claimRef(q.ID))
	if e != nil {
		return Result{}, e
	}
	if actual != o.ObjectID {
		return Result{}, failure("lost-ownership", "remote claim changed before materialization")
	}
	expectedAssignment := ""
	if a != nil {
		expectedAssignment = a.ObjectID
	}
	if e = r.requireAssignment(ctx, s, q.ID, expectedAssignment); e != nil {
		return Result{}, e
	}
	v, e := r.materializeConfirmed(ctx, s, m, o.ObjectID, q.Worktree)
	if e != nil {
		return Result{}, e
	}
	src := source("worktree", cfg.RepositoryID)
	return successful("materialize", &src, v, "Materialized "+q.ID+" at "+v.WorktreePath), nil
}
