package tsuzuri

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type historyStats struct{ Revisions, ParsedRecords, BlobBytes int }

// Only these facts survive full historical record parsing. Revision-specific
// paths and duplicate checks are never cached with the blob's parse result.
type historyRecord struct {
	id, project, state string
	isProject          bool
}
type historyRecordKey struct {
	object  string
	project bool
}
type historyTask struct{ path, state string }
type historyState struct {
	records          map[string]historyRecord
	tasks            map[string]historyTask
	seen             map[string]bool
	bytes, seenBytes int
}

func newHistoryState() *historyState {
	return &historyState{records: map[string]historyRecord{}, tasks: map[string]historyTask{}, seen: map[string]bool{}}
}
func historyRecordPath(path, root string) bool {
	return (path == root || strings.HasPrefix(path, root+"/")) && (strings.HasSuffix(path, "/project.md") || strings.HasSuffix(path, ".task.md"))
}
func historyKey(c historyChange) historyRecordKey {
	return historyRecordKey{c.object, strings.HasSuffix(c.path, "/project.md")}
}
func historyRecordBytes(path string, r historyRecord) int {
	return len(path) + len(r.id) + len(r.project) + len(r.state) + 128
}

func (s *historyState) apply(root string, changes []historyChange, reset bool, parsed map[historyRecordKey]historyRecord) error {
	before := map[string]historyTask{}
	remove := func(path string) {
		if r, ok := s.records[path]; ok {
			if !r.isProject {
				before[r.id] = s.tasks[r.id]
				delete(s.tasks, r.id)
			}
			s.bytes -= historyRecordBytes(path, r)
			delete(s.records, path)
		}
	}
	if reset {
		for path := range s.records {
			remove(path)
		}
	} else {
		for _, c := range changes {
			remove(c.path)
		}
	}
	added := []string{}
	for _, c := range changes {
		if c.mode == "000000" {
			continue
		}
		record, ok := parsed[historyKey(c)]
		if !ok {
			return invalid("missing parsed history record")
		}
		want := root + "/" + record.id + "/project.md"
		if !record.isProject {
			want = fmt.Sprintf("%s/%s/tasks/%s.task.md", root, record.project, record.id)
		}
		if c.path != want {
			return invalid("invalid historical record path %s", c.path)
		}
		if _, ok = s.records[c.path]; ok {
			return invalid("duplicate historical record path")
		}
		if !record.isProject {
			if _, ok = s.tasks[record.id]; ok {
				return invalid("duplicate task %s", record.id)
			}
			if _, wasPresent := before[record.id]; s.seen[record.id] && !wasPresent {
				return invalid("task ID %s was removed and reused", record.id)
			}
			path := strings.Clone(c.path)
			s.tasks[record.id] = historyTask{path, record.state}
			added = append(added, record.id)
		}
		s.bytes += historyRecordBytes(c.path, record)
		if s.bytes > maxOutput || len(s.records) >= maxRecords {
			return invalid("historical inventory exceeds supported bound")
		}
		s.records[strings.Clone(c.path)] = record
	}
	for id, old := range before {
		if terminal(old.state) {
			current, ok := s.tasks[id]
			if !ok || current != old {
				return invalid("terminal task `%s` changed state or path, or was removed", id)
			}
		}
	}
	for _, id := range added {
		if !s.seen[id] {
			s.seenBytes += len(id) + 64
			if s.seenBytes > maxOutput {
				return invalid("historical task identities exceed supported bound")
			}
			s.seen[id] = true
		}
	}
	return nil
}

type historyParseCache struct {
	records map[historyRecordKey]historyRecord
	bytes   int
}

func (r *repository) historyRecords(ctx context.Context, deltas []historyDelta, cache *historyParseCache) (map[historyRecordKey]historyRecord, error) {
	result := map[historyRecordKey]historyRecord{}
	wanted := map[string][]historyRecordKey{}
	for _, d := range deltas {
		for _, c := range d.changes {
			if c.mode == "000000" {
				continue
			}
			if c.mode != "100644" && c.mode != "100755" {
				return nil, invalid("record is not regular blob")
			}
			key := historyKey(c)
			if record, ok := cache.records[key]; ok {
				result[key] = record
				continue
			}
			if _, ok := result[key]; ok {
				continue
			}
			result[key] = historyRecord{}
			wanted[c.object] = append(wanted[c.object], key)
		}
	}
	bytes := 0
	err := r.historyBlobs(ctx, sortedKeys(wanted), func(id, body string) error {
		r.historyStats.BlobBytes += len(body)
		for _, key := range wanted[id] {
			record := historyRecord{isProject: key.project}
			if key.project {
				p, err := parseProject(body, "")
				if err != nil {
					return err
				}
				record.id = strings.Clone(p.ID)
				record.state = strings.Clone(p.State)
			} else {
				t, err := parseTask(body, "", true)
				if err != nil {
					return err
				}
				record.id = strings.Clone(t.ID)
				record.project = strings.Clone(t.Project)
				record.state = strings.Clone(t.State)
			}
			r.historyStats.ParsedRecords++
			size := historyRecordBytes("", record) + 128
			bytes += size
			if bytes > maxOutput {
				return invalid("history record batch exceeds supported bound")
			}
			if cache.bytes+size > historyBlobBatchBytes {
				cache.records = map[historyRecordKey]historyRecord{}
				cache.bytes = 0
			}
			// A substring key must not retain the entire raw-diff batch.
			cache.records[historyRecordKey{strings.Clone(key.object), key.project}] = record
			cache.bytes += size
			result[key] = record
		}
		return nil
	})
	return result, err
}

func historyRevisions(text string) ([]string, error) {
	ids := strings.Fields(text)
	if len(ids) > maxHistory {
		return nil, invalid("history exceeds supported bound")
	}
	for _, id := range ids {
		if !validObject(id) {
			return nil, invalid("invalid history revision")
		}
	}
	return ids, nil
}
func (r *repository) historyConfiguration(ctx context.Context, commit, repositoryID string) (string, map[string]Config, []string, error) {
	out, err := r.run(ctx, "log", "--first-parent", "--reverse", "--format=%H", commit, "--", ".tsuzuri.toml")
	if err != nil {
		return "", nil, nil, err
	}
	ids, err := historyRevisions(out)
	if err != nil {
		return "", nil, nil, err
	}
	adoption := ""
	configs := map[string]Config{}
	roots := map[string]bool{}
	bytes := 0
	for start := 0; start < len(ids); start += historyRevisionBatch {
		deltas, err := r.historyDiff(ctx, ids[start:min(start+historyRevisionBatch, len(ids))], []string{".tsuzuri.toml"})
		if err != nil {
			return "", nil, nil, err
		}
		objects := map[string]bool{}
		for _, d := range deltas {
			for _, c := range d.changes {
				if c.path == ".tsuzuri.toml" && (c.mode == "100644" || c.mode == "100755" || c.mode == "120000") {
					objects[c.object] = true
				}
			}
		}
		type parsedConfig struct {
			config Config
			err    error
		}
		parsed := map[string]parsedConfig{}
		err = r.historyBlobs(ctx, sortedKeys(objects), func(id, body string) error {
			bytes += len(body)
			if bytes > maxOutput {
				return invalid("configuration history exceeds supported bound")
			}
			c, e := parseConfig(body)
			parsed[id] = parsedConfig{c, e}
			return nil
		})
		if err != nil {
			return "", nil, nil, err
		}
		for _, d := range deltas {
			p := parsedConfig{err: invalid("missing historical configuration")}
			for _, change := range d.changes {
				if change.path == ".tsuzuri.toml" {
					if value, ok := parsed[change.object]; ok {
						p = value
					}
				}
			}
			if p.err != nil {
				if adoption != "" {
					return "", nil, nil, p.err
				}
				continue
			}
			if adoption == "" {
				if p.config.RepositoryID != repositoryID {
					continue
				}
				adoption = d.commit
			}
			if p.config.RepositoryID != repositoryID {
				return "", nil, nil, invalid("repository_id changed in first-parent history")
			}
			configs[d.commit] = p.config
			roots[p.config.TaskRoot] = true
		}
	}
	if adoption == "" {
		return "", nil, nil, invalid("no schema-1 adoption commit")
	}
	return adoption, configs, sortedKeys(roots), nil
}

func (r *repository) historyTree(ctx context.Context, commit, root string) ([]historyChange, error) {
	// Default -z output preserves literal paths. %(path) in a custom format
	// C-quotes tabs/newlines even with -z, hiding their record suffix.
	listing, err := r.run(ctx, "ls-tree", "-r", "-z", commit, "--", root)
	if err != nil {
		return nil, err
	}
	changes := []historyChange{}
	for listing != "" {
		line, rest, ok := strings.Cut(listing, "\x00")
		if !ok {
			return nil, invalid("truncated historical tree")
		}
		listing = rest
		header, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !validObject(fields[2]) {
			return nil, invalid("invalid historical tree")
		}
		if historyRecordPath(path, root) {
			changes = append(changes, historyChange{strings.Clone(path), strings.Clone(fields[0]), strings.Clone(fields[2])})
		}
		if len(changes) > maxRecords {
			return nil, invalid("too many historical records")
		}
	}
	return changes, nil
}

func (r *repository) validateHistory(ctx context.Context, commit, repositoryID string) error {
	if r.histories[commit+":"+repositoryID] {
		return nil
	}
	adoption, configs, roots, err := r.historyConfiguration(ctx, commit, repositoryID)
	if err != nil {
		return err
	}
	args := append([]string{"rev-list", "--first-parent", "--reverse", commit, "--", ".tsuzuri.toml"}, roots...)
	out, err := r.run(ctx, args...)
	if err != nil {
		return err
	}
	revisions, err := historyRevisions(out)
	if err != nil {
		return err
	}
	first := slices.Index(revisions, adoption)
	if first < 0 {
		return invalid("adoption missing from record history")
	}
	revisions = revisions[first:]
	state := newHistoryState()
	cache := &historyParseCache{records: map[historyRecordKey]historyRecord{}}
	root := ""
	for start := 0; start < len(revisions); start += historyRevisionBatch {
		batch := revisions[start:min(start+historyRevisionBatch, len(revisions))]
		deltas, err := r.historyDiff(ctx, batch, append([]string{".tsuzuri.toml"}, roots...))
		if err != nil {
			return err
		}
		resets := make([]bool, len(deltas))
		batchRoots := make([]string, len(deltas))
		descriptorBytes := 0
		for j := range deltas {
			d := &deltas[j]
			next := root
			if c, ok := configs[d.commit]; ok {
				next = c.TaskRoot
			}
			if next != root {
				d.changes, err = r.historyTree(ctx, d.commit, next)
				if err != nil {
					return err
				}
				resets[j] = true
			} else {
				relevant := []historyChange{}
				for _, c := range d.changes {
					if historyRecordPath(c.path, root) {
						relevant = append(relevant, c)
					}
				}
				d.changes = relevant
			}
			root = next
			batchRoots[j] = root
			for _, change := range d.changes {
				descriptorBytes += len(change.path) + len(change.object) + 128
			}
			if descriptorBytes > maxOutput {
				return invalid("history change batch exceeds supported bound")
			}
		}
		parsed, err := r.historyRecords(ctx, deltas, cache)
		if err != nil {
			return err
		}
		for j, d := range deltas {
			if err = ctx.Err(); err != nil {
				return invalid("history validation cancelled").causedBy(err)
			}
			if err = state.apply(batchRoots[j], d.changes, resets[j], parsed); err != nil {
				return err
			}
			r.historyStats.Revisions++
		}
	}
	// Exclude the adoption commit's ancestors in the revision walk itself. General
	// reachability alone never grants a proof to a rewritten or side-branch base.
	chain, err := r.run(ctx, "rev-list", "--first-parent", commit, "^"+adoption)
	if err != nil {
		return err
	}
	// The byte-bounded output may include non-record commits; they need no replay.
	parents := strings.Fields(chain)
	for _, id := range parents {
		if !validObject(id) {
			return invalid("invalid history proof revision")
		}
		r.histories[id+":"+repositoryID] = true
	}
	r.histories[adoption+":"+repositoryID] = true
	// Only a completed walk proves ancestry to this exact tip. Validation of a
	// different branch must never imply that its commits belong to this history.
	for _, id := range append(parents, adoption) {
		if len(r.cache.firstParents) >= maxCacheEntries {
			break
		}
		if r.cache.firstParents == nil {
			r.cache.firstParents = map[ancestryKey]struct{}{}
		}
		r.cache.firstParents[ancestryKey{strings.Clone(id), strings.Clone(commit)}] = struct{}{}
	}
	return nil
}
