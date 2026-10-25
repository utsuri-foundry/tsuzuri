package tsuzuri

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

func newInventory(c Config) *inventory {
	return &inventory{Config: c, Projects: map[string]Project{}, Tasks: map[string]Task{}, Documents: map[string]string{}, Digests: map[string]string{}, Source: source("worktree", c.RepositoryID)}
}
func (r *repository) config(ctx context.Context) (Config, error) {
	s, e := regularFile(filepath.Join(r.root, ".tsuzuri.toml"))
	if e != nil {
		return Config{}, e
	}
	c, e := parseConfig(s)
	if e != nil {
		return Config{}, e
	}
	return c, r.checkConfiguration(c)
}
func (r *repository) worktree(ctx context.Context) (*inventory, error) {
	c, e := r.config(ctx)
	if e != nil {
		return nil, e
	}
	i := newInventory(c)
	root := filepath.Join(r.root, filepath.FromSlash(c.TaskRoot))
	if e = physicalPath(r.root, root); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return nil, invalid("task root: %v", e).causedBy(e)
	}
	count := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, invalid("symlink under task root")
		}
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(root, entry.Name(), "project.md")
		paths := []string{p}
		td := filepath.Join(root, entry.Name(), "tasks")
		ts, err := os.ReadDir(td)
		if err != nil && !os.IsNotExist(err) {
			return nil, invalid("task directory: %v", err).causedBy(err)
		}
		if err == nil {
			if e = physicalPath(r.root, td); e != nil {
				return nil, e
			}
		}
		for _, t := range ts {
			if t.Type()&os.ModeSymlink != 0 {
				return nil, invalid("symlink in task directory")
			}
			if !t.IsDir() && strings.HasSuffix(t.Name(), ".task.md") {
				paths = append(paths, filepath.Join(td, t.Name()))
			}
		}
		for _, p := range paths {
			count++
			if count > maxRecords {
				return nil, invalid("too many records")
			}
			s, e := regularFile(p)
			if e != nil {
				return nil, e
			}
			d, e := r.blob(ctx, s, false)
			if e != nil {
				return nil, e
			}
			rel, e := filepath.Rel(r.root, p)
			if e != nil {
				return nil, invalid("record path: %v", e).causedBy(e)
			}
			if e = i.add(filepath.ToSlash(rel), s, d, false); e != nil {
				return nil, e
			}
		}
	}
	return i, i.validate()
}
func physicalPath(root, path string) error {
	rel, e := filepath.Rel(root, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return invalid("path outside repository")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if e != nil {
			return invalid("cannot inspect path: %v", e).causedBy(e)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return invalid("symlink in record path")
		}
	}
	return nil
}
func (r *repository) readObjects(ctx context.Context, ids []string, kind string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	for _, id := range ids {
		if !validObject(id) {
			return nil, invalid("invalid object ID")
		}
	}
	out, e := r.input(ctx, strings.Join(ids, "\n")+"\n", nil, "cat-file", "--batch")
	if e != nil {
		return nil, e
	}
	var values []string
	for _, id := range ids {
		header, rest, ok := strings.Cut(out, "\n")
		if !ok {
			return nil, invalid("truncated Git object header")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != id || fields[1] != kind {
			return nil, invalid("unexpected Git object header")
		}
		size, e := strconv.Atoi(fields[2])
		if e != nil || size < 0 || size >= len(rest) || rest[size] != '\n' {
			return nil, invalid("invalid Git object size")
		}
		if kind == "blob" && size > maxRecord {
			return nil, invalid("record exceeds size bound")
		}
		if !utf8.ValidString(rest[:size]) {
			return nil, invalid("invalid UTF-8 object")
		}
		values = append(values, rest[:size])
		out = rest[size+1:]
	}
	if out != "" {
		return nil, invalid("trailing Git object output")
	}
	return values, nil
}
func (r *repository) tree(ctx context.Context, rev string, legacy, history bool) (*inventory, error) {
	commit, e := r.oid(ctx, rev+"^{commit}")
	if e != nil {
		return nil, e
	}
	key := fmt.Sprintf("%s:%t", commit, legacy)
	if i, ok := r.trees[key]; ok {
		if history {
			if e = i.validate(); e != nil {
				return nil, e
			}
			if e = r.validateHistory(ctx, commit, i.Config.RepositoryID); e != nil {
				return nil, e
			}
		}
		return i, nil
	}
	cfg, e := r.show(ctx, commit, ".tsuzuri.toml")
	if e != nil {
		return nil, e
	}
	c, e := parseConfig(cfg)
	if e != nil {
		return nil, e
	}
	i := newInventory(c)
	cfgID, e := r.oid(ctx, commit+":.tsuzuri.toml")
	if e != nil {
		return nil, e
	}
	i.Source = source("git-tree", c.RepositoryID)
	i.Source.BaseObjectID = stringPtr(commit)
	i.Source.ConfigurationObjectID = stringPtr(cfgID)
	listing, e := r.run(ctx, "ls-tree", "-r", "-z", commit, "--", c.TaskRoot)
	if e != nil {
		return nil, e
	}
	var paths, ids []string
	for _, line := range strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00") {
		if line == "" {
			continue
		}
		header, path, ok := strings.Cut(line, "\t")
		f := strings.Fields(header)
		if !ok || len(f) != 3 {
			return nil, invalid("malformed tree listing")
		}
		if !strings.HasSuffix(path, "/project.md") && !strings.HasSuffix(path, ".task.md") {
			continue
		}
		if f[0] != "100644" && f[0] != "100755" {
			return nil, invalid("record is not regular blob")
		}
		paths = append(paths, path)
		ids = append(ids, f[2])
		if len(paths) > maxRecords {
			return nil, invalid("too many records")
		}
	}
	texts, e := r.objects(ctx, ids, "blob")
	if e != nil {
		return nil, e
	}
	for idx, p := range paths {
		if e = r.addRecord(i, p, texts[idx], ids[idx], legacy); e != nil {
			return nil, e
		}
	}
	r.trees[key] = i
	if history {
		if e = i.validate(); e != nil {
			return nil, e
		}
		if e = r.validateHistory(ctx, commit, c.RepositoryID); e != nil {
			return nil, e
		}
	}
	return i, nil
}
