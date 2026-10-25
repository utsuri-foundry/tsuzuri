package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type area struct {
	Name, Responsibility, NonResponsibility, Contracts, Check string
	Files, AllowedImports, DependsOn                          []string
}
type budget struct{ Warning, Maximum int }
type configuration struct {
	Schema                                            int
	Status                                            string
	SourceRoots, SourceExtensions, IgnoredDirectories []string
	Layers                                            []area
	SourceBudgets                                     map[string]budget
	Classification                                    struct{ TestPathPrefixes, FacadeFiles, FacadeDirectories []string }
	ForbiddenModuleNames, RequiredDocuments           []string
}
type exception struct {
	Rule, Path, Owner, Reason, Created, Expires, TaskID string
	Ceiling                                             int
}
type exceptions struct {
	Schema     int
	Exceptions []exception
}

// Repository policy is trusted, but malformed policy must fail closed. All reads
// are regular files, bounded to 4 MiB; the scan is bounded to 100,000 entries.
func read(root, path string) ([]byte, error) {
	if !fs.ValidPath(path) {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	p := filepath.Join(root, filepath.FromSlash(path))
	s, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !s.Mode().IsRegular() || s.Size() > 4<<20 {
		return nil, fmt.Errorf("invalid file %s", path)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("oversized file %s", path)
	}
	return b, nil
}
func decode(root, path string, dst any) error {
	b, err := read(root, path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(dst); err != nil {
		return err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON in %s", path)
	}
	return nil
}
func beneath(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
func class(c configuration, path string) string {
	if strings.HasSuffix(path, "_test.go") || beneath(path, c.Classification.TestPathPrefixes) {
		return "test"
	}
	if slices.Contains(c.Classification.FacadeFiles, path) || beneath(path, c.Classification.FacadeDirectories) {
		return "facade"
	}
	return "production"
}
func check(ctx context.Context, root string, now time.Time, warnings io.Writer) error {
	var c configuration
	var es exceptions
	if err := decode(root, "architecture.json", &c); err != nil {
		return err
	}
	if err := decode(root, "architecture-exceptions.json", &es); err != nil {
		return err
	}
	if c.Schema != 1 || c.Status != "active" || es.Schema != 1 || len(c.SourceRoots) == 0 || len(c.SourceExtensions) == 0 || len(c.RequiredDocuments) == 0 {
		return fmt.Errorf("incomplete architecture policy")
	}
	for _, kind := range []string{"production", "facade", "test"} {
		b := c.SourceBudgets[kind]
		if b.Warning <= 0 || b.Maximum < b.Warning {
			return fmt.Errorf("invalid %s budget", kind)
		}
	}
	for _, doc := range c.RequiredDocuments {
		b, err := read(root, doc)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(b)) == 0 {
			return fmt.Errorf("empty document %s", doc)
		}
	}
	owners := map[string]area{}
	names := map[string]bool{}
	for _, a := range c.Layers {
		if a.Name == "" || names[a.Name] || a.Responsibility == "" || a.NonResponsibility == "" || a.Contracts == "" || a.Check == "" || len(a.Files) == 0 {
			return fmt.Errorf("incomplete or duplicate area %q", a.Name)
		}
		names[a.Name] = true
		for _, p := range a.Files {
			if !fs.ValidPath(p) || owners[p].Name != "" {
				return fmt.Errorf("invalid or duplicate ownership: %s", p)
			}
			owners[p] = a
		}
	}
	for _, a := range c.Layers {
		for _, dep := range a.DependsOn {
			if !names[dep] || dep == a.Name {
				return fmt.Errorf("invalid dependency %s -> %s", a.Name, dep)
			}
		}
	}
	if err := checkCycles(c.Layers); err != nil {
		return err
	}
	exempt := map[string]exception{}
	for _, e := range es.Exceptions {
		if err := validateException(root, e, now); err != nil {
			return err
		}
		if owners[e.Path].Name == "" || exempt[e.Path].Path != "" {
			return fmt.Errorf("orphan or duplicate exception %s", e.Path)
		}
		exempt[e.Path] = e
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	entries := 0
	for _, sourceRoot := range c.SourceRoots {
		if !fs.ValidPath(sourceRoot) {
			return fmt.Errorf("invalid source root")
		}
		err := filepath.WalkDir(filepath.Join(root, sourceRoot), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			entries++
			if entries > 100000 {
				return fmt.Errorf("source scan exceeds bound")
			}
			if d.IsDir() && path != root && slices.Contains(c.IgnoredDirectories, d.Name()) {
				return filepath.SkipDir
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.TrimPrefix(filepath.Ext(path), ".")
			if !slices.Contains(c.SourceExtensions, ext) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				return fmt.Errorf("overlapping source roots: %s", rel)
			}
			seen[rel] = true
			a := owners[rel]
			if a.Name == "" {
				return fmt.Errorf("unowned source %s", rel)
			}
			for _, component := range strings.Split(strings.TrimSuffix(rel, filepath.Ext(rel)), "/") {
				if slices.Contains(c.ForbiddenModuleNames, component) {
					return fmt.Errorf("forbidden module name %s", rel)
				}
			}
			body, err := read(root, rel)
			if err != nil {
				return err
			}
			lines := bytes.Count(body, []byte("\n"))
			if len(body) > 0 && body[len(body)-1] != '\n' {
				lines++
			}
			b := c.SourceBudgets[class(c, rel)]
			if lines > b.Warning {
				if _, err := fmt.Fprintf(warnings, "%s: %d lines; cohesion review required\n", rel, lines); err != nil {
					return err
				}
			}
			if lines > b.Maximum {
				e := exempt[rel]
				if e.Ceiling < lines {
					return fmt.Errorf("source budget exceeded: %s (%d > %d)", rel, lines, b.Maximum)
				}
				used[rel] = true
			}
			if ext == "go" {
				return checkImports(c, a, rel, body)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for p := range owners {
		if !seen[p] {
			return fmt.Errorf("owned source missing or excluded: %s", p)
		}
	}
	for p := range exempt {
		if !used[p] {
			return fmt.Errorf("unused exception: %s", p)
		}
	}
	return nil
}
func checkCycles(areas []area) error {
	dependencies := map[string][]string{}
	for _, a := range areas {
		dependencies[a.Name] = a.DependsOn
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("cyclic area dependency: %s", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, dep := range dependencies[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, a := range areas {
		if err := visit(a.Name); err != nil {
			return err
		}
	}
	return nil
}
func checkImports(c configuration, a area, path string, body []byte) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ImportsOnly)
	if err != nil {
		return err
	}
	if slices.Contains(c.ForbiddenModuleNames, f.Name.Name) {
		return fmt.Errorf("forbidden package name %s", path)
	}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}
		if !slices.Contains(a.AllowedImports, p) {
			return fmt.Errorf("forbidden import %s in %s (%s)", p, path, a.Name)
		}
	}
	return nil
}
func validateException(root string, e exception, now time.Time) error {
	created, ce := time.Parse(time.DateOnly, e.Created)
	expires, ee := time.Parse(time.DateOnly, e.Expires)
	day := now.UTC().Truncate(24 * time.Hour)
	if e.Rule != "source-budget" || !fs.ValidPath(e.Path) || strings.ContainsAny(e.Path, "*?[") || strings.TrimSpace(e.Owner) == "" || strings.TrimSpace(e.Reason) == "" || e.Ceiling <= 0 || ce != nil || ee != nil || created.After(day) || !expires.After(day) || !expires.After(created) || e.TaskID == "" || strings.ContainsAny(e.TaskID, "/\\.") {
		return fmt.Errorf("malformed or expired exception: %s", e.Path)
	}
	body, err := read(root, "tasks/projects/tsuzuri-go/tasks/"+e.TaskID+".task.md")
	if err != nil {
		return err
	}
	parts := strings.SplitN(string(body), "+++", 3)
	if len(parts) != 3 || parts[0] != "" {
		return fmt.Errorf("invalid removal task")
	}
	var task struct{ ID, State string }
	if err := toml.Unmarshal([]byte(parts[1]), &task); err != nil {
		return err
	}
	if task.ID != e.TaskID || !slices.Contains([]string{"backlog", "ready", "blocked"}, task.State) {
		return fmt.Errorf("exception requires matching nonterminal removal task")
	}
	return nil
}
