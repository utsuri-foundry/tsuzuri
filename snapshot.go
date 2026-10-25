package tsuzuri

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type observedRef struct {
	TaskID   string `json:"taskId"`
	RefName  string `json:"refName"`
	ObjectID string `json:"objectId"`
}
type snapshot struct {
	id, base, baseRef, claimPrefix, assignmentPrefix, remote, owner string
	claims, assignments                                             map[string]observedRef
	loaded                                                          *inventory
}

func (r *repository) configValues(ctx context.Context, key string) ([]string, error) {
	o, e := r.exec(ctx, "", nil, "config", "--local", "--get-all", key)
	if e != nil {
		return nil, e
	}
	if o.code == 1 {
		return []string{}, nil
	}
	if o.code != 0 {
		return nil, failure("remote", o.diagnostic)
	}
	return strings.Split(strings.TrimSuffix(o.text, "\n"), "\n"), nil
}
func (r *repository) configValue(ctx context.Context, key string) (string, error) {
	v, e := r.configValues(ctx, key)
	if e != nil {
		return "", e
	}
	if len(v) > 1 {
		return "", failure("remote", "configuration must have at most one value: "+key)
	}
	if len(v) == 0 {
		return "", nil
	}
	return v[0], nil
}
func (r *repository) remotePolicy(ctx context.Context) (string, string, error) {
	remote, e := r.configValue(ctx, "tsuzuri.remote")
	if e != nil {
		return "", "", e
	}
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n") {
		return "", "", failure("remote", "missing or invalid clone-local tsuzuri.remote")
	}
	urls, e := r.configValues(ctx, "remote."+remote+".url")
	if e != nil {
		return "", "", e
	}
	push, e := r.configValues(ctx, "remote."+remote+".pushurl")
	if e != nil {
		return "", "", e
	}
	if len(urls) != 1 || len(push) > 1 || (len(push) == 1 && push[0] != urls[0]) {
		return "", "", failure("remote", "remote must use one identical fetch and push URL")
	}
	// Git applies insteadOf/pushInsteadOf and pushurl from all configuration
	// scopes. Raw clone-local URL equality alone does not identify one store.
	fetchURLs, e := r.run(ctx, "remote", "get-url", "--all", remote)
	if e != nil {
		return "", "", failure("remote", "cannot resolve effective fetch destination").causedBy(e)
	}
	pushURLs, e := r.run(ctx, "remote", "get-url", "--push", "--all", remote)
	if e != nil {
		return "", "", failure("remote", "cannot resolve effective push destination").causedBy(e)
	}
	fetchDestinations := strings.Split(strings.TrimSpace(fetchURLs), "\n")
	pushDestinations := strings.Split(strings.TrimSpace(pushURLs), "\n")
	if len(fetchDestinations) != 1 || len(pushDestinations) != 1 || fetchDestinations[0] == "" || fetchDestinations[0] != pushDestinations[0] {
		return "", "", failure("remote", "effective fetch and push destinations must be identical")
	}
	if r.policy.ExpectedRemoteURL != "" && fetchDestinations[0] != r.policy.ExpectedRemoteURL {
		return "", "", invalid("remote destination differs from operation policy")
	}
	for _, key := range []string{"remotes." + remote, "remote." + remote + ".pushOption"} {
		v, e := r.configValues(ctx, key)
		if e != nil {
			return "", "", e
		}
		if len(v) != 0 {
			return "", "", failure("remote", "remote groups and push options are unsupported")
		}
	}
	mirror, e := r.configValue(ctx, "remote."+remote+".mirror")
	if e != nil {
		return "", "", e
	}
	if mirror == "true" {
		return "", "", failure("remote", "mirror remote unsupported")
	}
	owner, e := r.configValue(ctx, "tsuzuri.owner")
	return remote, owner, e
}
func (r *repository) fetch(ctx context.Context, remote string, atomic bool, refspecs ...string) error {
	a := []string{"-c", "fetch.prune=false", "fetch", "--no-tags", "--no-recurse-submodules"}
	if atomic {
		a = append(a, "--atomic")
	}
	a = append(a, remote)
	a = append(a, refspecs...)
	o, e := r.exec(ctx, "", nil, a...)
	if e != nil {
		return failure("remote", e.Error()).causedBy(e)
	}
	if o.code != 0 {
		return failure("remote", o.diagnostic)
	}
	return nil
}
func (r *repository) snapshot(ctx context.Context) (*snapshot, error) {
	version, e := r.run(ctx, "version")
	if e != nil {
		return nil, e
	}
	f := strings.Fields(version)
	if len(f) < 3 {
		return nil, failure("remote", "unrecognized Git version")
	}
	v := strings.Split(f[2], ".")
	if len(v) < 2 {
		return nil, failure("remote", "unrecognized Git version")
	}
	major, e1 := strconv.Atoi(v[0])
	minor, e2 := strconv.Atoi(v[1])
	if e1 != nil || e2 != nil || major < 2 || (major == 2 && minor < 40) {
		return nil, failure("remote", "Git 2.40 or newer required")
	}
	bootstrap, e := r.config(ctx)
	if e != nil {
		return nil, e
	}
	remote, owner, e := r.remotePolicy(ctx)
	if e != nil {
		return nil, e
	}
	bootref := "refs/tsuzuri/bootstrap/" + strings.ReplaceAll(uuid(), "-", "") + "/base"
	if e = r.fetch(ctx, remote, false, "+refs/heads/"+bootstrap.BaseBranch+":"+bootref); e != nil {
		return nil, e
	}
	boot, e := r.oid(ctx, bootref)
	if e != nil {
		return nil, e
	}
	text, e := r.show(ctx, boot, ".tsuzuri.toml")
	if e != nil {
		return nil, e
	}
	c, e := parseConfig(text)
	if e != nil {
		return nil, e
	}
	if e = r.checkConfiguration(c); e != nil {
		return nil, e
	}
	if c.RepositoryID != bootstrap.RepositoryID || c.BaseBranch != bootstrap.BaseBranch {
		return nil, failure("remote", "configuration-drift: working bootstrap differs from remote base")
	}
	for _, ref := range []string{"refs/heads/" + c.BaseBranch, c.claimRef("probe"), c.assignmentRef("probe")} {
		if _, e = r.run(ctx, "check-ref-format", ref); e != nil {
			return nil, failure("remote", "invalid configured ref")
		}
	}
	assignmentPrefix := "refs/heads/" + c.ClaimNamespace + "-assignments"
	claimPrefix := "refs/heads/" + c.ClaimNamespace
	baseRef := "refs/heads/" + c.BaseBranch
	overlaps := func(a, b string) bool { return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") }
	if overlaps(assignmentPrefix, baseRef) || overlaps(claimPrefix, baseRef) || overlaps(claimPrefix, assignmentPrefix) {
		return nil, failure("remote", "base, claim and assignment namespaces must not overlap")
	}
	s := &snapshot{id: strings.ReplaceAll(uuid(), "-", ""), remote: remote, owner: owner}
	prefix := "refs/tsuzuri/snapshots/" + s.id
	s.baseRef = prefix + "/base"
	s.claimPrefix = prefix + "/claims"
	s.assignmentPrefix = prefix + "/assignments"
	if e = r.fetch(ctx, remote, true, "+refs/heads/"+c.BaseBranch+":"+s.baseRef, "+refs/heads/"+c.ClaimNamespace+"/*:"+s.claimPrefix+"/*", "+"+assignmentPrefix+"/*:"+s.assignmentPrefix+"/*"); e != nil {
		return nil, e
	}
	s.base, e = r.oid(ctx, s.baseRef)
	if e != nil {
		return nil, e
	}
	if s.base != boot {
		return nil, failure("remote", "base changed during snapshot construction")
	}
	s.loaded, e = r.tree(ctx, s.base, false, true)
	if e != nil {
		return nil, e
	}
	s.claims, e = r.snapshotRefs(ctx, s.claimPrefix, c, false)
	if e != nil {
		return nil, e
	}
	s.assignments, e = r.snapshotRefs(ctx, s.assignmentPrefix, c, true)
	if e != nil {
		return nil, e
	}
	src := source("remote-snapshot", c.RepositoryID)
	src.BaseObjectID = stringPtr(s.base)
	src.ConfigurationObjectID = s.loaded.Source.ConfigurationObjectID
	src.SnapshotID = stringPtr(s.id)
	src.RefreshResult = stringPtr("success")
	for _, id := range sortedKeys(s.claims) {
		src.ObservedClaimObjectIDs = append(src.ObservedClaimObjectIDs, s.claims[id].ObjectID)
	}
	for _, id := range sortedKeys(s.assignments) {
		src.ObservedAssignmentObjectIDs = append(src.ObservedAssignmentObjectIDs, s.assignments[id].ObjectID)
	}
	copyLoaded := *s.loaded
	copyLoaded.Source = src
	s.loaded = &copyLoaded
	if e = r.remove(ctx, bootref, boot); e != nil {
		return nil, e
	}
	return s, nil
}
func sortedKeys[V any](m map[string]V) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	slices.Sort(a)
	return a
}
func (r *repository) snapshotRefs(ctx context.Context, prefix string, c Config, assignment bool) (map[string]observedRef, error) {
	out, e := r.run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", prefix+"/")
	if e != nil {
		return nil, e
	}
	m := map[string]observedRef{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !strings.HasPrefix(f[0], prefix+"/") || !validObject(f[1]) {
			return nil, failure("remote", "malformed snapshot ref")
		}
		id := strings.TrimPrefix(f[0], prefix+"/")
		if id == "" || strings.Contains(id, "/") {
			return nil, failure("remote", "invalid task ref in namespace")
		}
		ref := c.claimRef(id)
		if assignment {
			ref = c.assignmentRef(id)
		}
		m[id] = observedRef{id, ref, f[1]}
	}
	return m, nil
}
func (r *repository) anchor(ctx context.Context, s *snapshot, observed observedRef) (ClaimMetadata, *TaskRevision, error) {
	var zero ClaimMetadata
	history, e := r.run(ctx, "log", "--format=%H%x09%s", observed.ObjectID)
	if e != nil {
		return zero, nil, e
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(history), "\n") {
		id, subject, ok := strings.Cut(line, "\t")
		if ok && subject == "tsuzuri claim "+observed.TaskID {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return zero, nil, failure("claim-conflict", fmt.Sprintf("expected one claim anchor, found %d", len(ids)))
	}
	// A single anchor is read with show; batch cat-file is reserved for the
	// record inventory and assignment chains.
	commit, e := r.run(ctx, "show", "--no-patch", "--no-notes", "--format=format:tree %T%nparent %P%n%n%B", ids[0])
	if e != nil {
		return zero, nil, e
	}
	header, message, ok := strings.Cut(commit, "\n\n")
	if !ok {
		return zero, nil, invalid("invalid commit object")
	}
	m, e := parseClaim(message, ids[0])
	if e != nil {
		return zero, nil, e
	}
	if m.TaskID != observed.TaskID || m.RepositoryID != s.loaded.Config.RepositoryID {
		return zero, nil, failure("claim-conflict", "claim identity differs")
	}
	var parents []string
	tree := ""
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "parent ") {
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		}
		if strings.HasPrefix(line, "tree ") {
			tree = strings.TrimPrefix(line, "tree ")
		}
	}
	parentTree, e := r.oid(ctx, m.BaseObjectID+"^{tree}")
	if e != nil {
		return zero, nil, e
	}
	if len(parents) != 1 || parents[0] != m.BaseObjectID || tree != parentTree {
		return zero, nil, failure("claim-conflict", "anchor must have unchanged tree and exact declared parent")
	}
	base, e := r.tree(ctx, m.BaseObjectID, true, true)
	if e != nil {
		return zero, nil, e
	}
	t, ok := base.Tasks[m.TaskID]
	if !ok || !base.claimable(t) {
		return zero, nil, failure("claim-conflict", "task not claimable at anchor base")
	}
	revision := &TaskRevision{m.BaseObjectID, t.Path, base.Digests[t.Path]}
	return m, revision, nil
}
func (r *repository) assignment(ctx context.Context, s *snapshot, o observedRef, m ClaimMetadata) (AssignmentMetadata, error) {
	var zero AssignmentMetadata
	history, e := r.run(ctx, "rev-list", "--first-parent", "--parents", "--max-count=1025", o.ObjectID, "^"+m.AnchorObjectID)
	if e != nil {
		return zero, e
	}
	lines := strings.Split(strings.TrimSpace(history), "\n")
	if len(lines) == 0 || len(lines) > 1024 {
		return zero, failure("claim-conflict", "assignment chain exceeds supported bound")
	}
	var ids, parents []string
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) != 2 || !validObject(f[0]) || !validObject(f[1]) {
			return zero, failure("claim-conflict", "assignment chain must have single parents")
		}
		ids = append(ids, f[0])
		parents = append(parents, f[1])
	}
	if ids[0] != o.ObjectID {
		return zero, failure("claim-conflict", "assignment tip omitted")
	}
	for idx, parent := range parents {
		expected := m.AnchorObjectID
		if idx+1 < len(ids) {
			expected = ids[idx+1]
		}
		if parent != expected {
			return zero, failure("claim-conflict", "assignment chain does not terminate at claim anchor")
		}
	}
	commits, e := r.objects(ctx, ids, "commit")
	if e != nil {
		return zero, e
	}
	tree, e := r.oid(ctx, m.AnchorObjectID+"^{tree}")
	if e != nil {
		return zero, e
	}
	seen := map[string]bool{}
	var latest AssignmentMetadata
	for idx, commit := range commits {
		header, message, ok := strings.Cut(commit, "\n\n")
		if !ok || !strings.HasPrefix(header, "tree "+tree+"\n") {
			return zero, failure("claim-conflict", "assignment is not metadata-only")
		}
		a, e := parseAssignment(message, ids[idx])
		if e != nil {
			return zero, e
		}
		if a.RepositoryID != s.loaded.Config.RepositoryID || a.TaskID != o.TaskID || a.ClaimID != m.ClaimID || a.PreviousObjectID != parents[idx] || seen[a.AssignmentID] {
			return zero, failure("claim-conflict", "assignment identity, predecessor or generation differs")
		}
		seen[a.AssignmentID] = true
		if idx == 0 {
			latest = a
		}
	}
	return latest, nil
}
func (r *repository) drift(ctx context.Context, s *snapshot, m ClaimMetadata) []string {
	drift := []string{}
	if m.BaseObjectID == s.base {
		return drift
	}
	out, e := r.exec(ctx, "", nil, "merge-base", "--is-ancestor", m.BaseObjectID, s.base)
	if e == nil && out.code == 0 {
		drift = append(drift, "compatible-base-advancement")
	} else {
		drift = append(drift, "history-rewritten")
	}
	before, e := r.tree(ctx, m.BaseObjectID, true, true)
	if e != nil {
		return append(drift, "claimed-base-unreadable")
	}
	if !reflect.DeepEqual(before.Config, s.loaded.Config) {
		drift = append(drift, "coordination-configuration-changed")
	}
	a, aok := before.Tasks[m.TaskID]
	b, bok := s.loaded.Tasks[m.TaskID]
	if !aok || !bok || before.Digests[a.Path] != s.loaded.Digests[b.Path] {
		drift = append(drift, "task-intent-changed")
	}
	if aok && bok {
		if before.dependency(a) != s.loaded.dependency(b) {
			drift = append(drift, "dependency-result-changed")
		}
		if before.Projects[a.Project].State != s.loaded.Projects[b.Project].State {
			drift = append(drift, "project-state-changed")
		}
	}
	slices.Sort(drift)
	return drift
}
func (r *repository) observe(ctx context.Context, s *snapshot) []ClaimObservation {
	result := []ClaimObservation{}
	for _, id := range sortedKeys(s.claims) {
		o := s.claims[id]
		c := ClaimObservation{TaskID: id, RefName: o.RefName, ObjectID: o.ObjectID, Status: "invalid", Drift: []string{}}
		m, rev, e := r.anchor(ctx, s, o)
		if e != nil {
			c.Error = stringPtr(e.Error())
		} else {
			c.Metadata = &m
			c.ClaimedTask = rev
			c.Status = "active"
			c.Valid = true
			if t, ok := s.loaded.Tasks[id]; !ok {
				c.Status = "invalid"
				c.Valid = false
			} else if terminal(t.State) {
				c.Status = "stale-terminal"
			}
			c.Drift = r.drift(ctx, s, m)
		}
		if a, ok := s.assignments[id]; ok {
			obs := &AssignmentObservation{TaskID: id, RefName: a.RefName, ObjectID: a.ObjectID, Status: "invalid"}
			if c.Metadata == nil {
				obs.Error = stringPtr("assignment claim is invalid")
			} else {
				metadata, err := r.assignment(ctx, s, a, m)
				if err != nil {
					obs.Error = stringPtr(err.Error())
				} else {
					obs.Valid = true
					obs.Status = metadata.State
					obs.Metadata = &metadata
				}
			}
			c.Assignment = obs
		}
		result = append(result, c)
	}
	return result
}
func (s *snapshot) orphans() []AssignmentObservation {
	result := []AssignmentObservation{}
	for _, id := range sortedKeys(s.assignments) {
		if _, ok := s.claims[id]; !ok {
			a := s.assignments[id]
			result = append(result, AssignmentObservation{TaskID: id, RefName: a.RefName, ObjectID: a.ObjectID, Status: "orphan", Error: stringPtr("assignment has no matching remote claim")})
		}
	}
	return result
}
