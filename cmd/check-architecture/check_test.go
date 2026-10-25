package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, root, path, text string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func save(t *testing.T, root, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, path, string(b))
}
func fixturePolicy(t *testing.T) (string, configuration, exceptions) {
	t.Helper()
	root := t.TempDir()
	put(t, root, "records.go", "package records\n")
	put(t, root, "README.md", "Repository contracts\n")
	put(t, root, "tasks/projects/tsuzuri-go/tasks/remove-limit.task.md", "+++\nid = \"remove-limit\"\nstate = \"ready\"\n+++\n")
	c := configuration{Schema: 1, Status: "active", SourceRoots: []string{"."}, SourceExtensions: []string{"go"}, IgnoredDirectories: []string{"build"}, RequiredDocuments: []string{"README.md"}, ForbiddenModuleNames: []string{"utils"}, SourceBudgets: map[string]budget{"production": {2, 3}, "facade": {1, 2}, "test": {4, 5}}}
	c.Layers = []area{{Name: "records", Responsibility: "parse", NonResponsibility: "effects", Contracts: "records", Check: "go test ./...", Files: []string{"records.go"}, AllowedImports: []string{"strings"}}}
	return root, c, exceptions{Schema: 1}
}
func TestArchitectureRejectsRegressions(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*testing.T, string, *configuration, *exceptions)
	}{
		{"valid", "", func(*testing.T, string, *configuration, *exceptions) {}},
		{"new-source", "unowned", func(t *testing.T, r string, c *configuration, e *exceptions) {
			put(t, r, "extra.go", "package records\n")
		}},
		{"duplicate-owner", "duplicate ownership", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].Files = append(c.Layers[0].Files, "records.go")
		}},
		{"missing-owner-file", "owned source missing", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].Files = append(c.Layers[0].Files, "missing.go")
		}},
		{"unknown-area-dependency", "invalid dependency", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].DependsOn = []string{"missing"}
		}},
		{"direct-effect-import", "forbidden import", func(t *testing.T, r string, c *configuration, e *exceptions) {
			put(t, r, "records.go", "package records\nimport \"os/exec\"\n")
		}},
		{"tagged-effect-import", "forbidden import", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.SourceBudgets["production"] = budget{8, 9}
			put(t, r, "records.go", "//go:build windows\n\npackage records\nimport \"os/exec\"\n")
		}},
		{"allowed-import", "", func(t *testing.T, r string, c *configuration, e *exceptions) {
			put(t, r, "records.go", "package records\nimport \"strings\"\n")
		}},
		{"required-doc", "README.md", func(t *testing.T, r string, c *configuration, e *exceptions) {
			if err := os.Remove(filepath.Join(r, "README.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty-doc", "empty document", func(t *testing.T, r string, c *configuration, e *exceptions) { put(t, r, "README.md", "\n") }},
		{"generic-package", "forbidden package", func(t *testing.T, r string, c *configuration, e *exceptions) {
			put(t, r, "records.go", "package utils\n")
		}},
		{"generic-module", "forbidden module", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].Files = append(c.Layers[0].Files, "utils/check.go")
			put(t, r, "utils/check.go", "package records\n")
		}},
		{"production-budget", "source budget exceeded", func(t *testing.T, r string, c *configuration, e *exceptions) {
			put(t, r, "records.go", "package records\n\n\n\n")
		}},
		{"facade-budget", "source budget exceeded", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Classification.FacadeFiles = []string{"records.go"}
			put(t, r, "records.go", "package records\n\n\n")
		}},
		{"colocated-test-class", "", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].Files = append(c.Layers[0].Files, "records_test.go")
			put(t, r, "records_test.go", "package records\n\n\n\n\n")
		}},
		{"test-budget", "source budget exceeded", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.Layers[0].Files = append(c.Layers[0].Files, "records_test.go")
			put(t, r, "records_test.go", "package records\n\n\n\n\n\n")
		}},
		{"overlapping-roots", "overlapping source roots", func(t *testing.T, r string, c *configuration, e *exceptions) { c.SourceRoots = []string{".", "."} }},
		{"inactive-policy", "incomplete architecture", func(t *testing.T, r string, c *configuration, e *exceptions) { c.Status = "scaffold" }},
		{"invalid-budget", "invalid production budget", func(t *testing.T, r string, c *configuration, e *exceptions) {
			c.SourceBudgets["production"] = budget{5, 3}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, c, e := fixturePolicy(t)
			test.change(t, root, &c, &e)
			save(t, root, "architecture.json", c)
			save(t, root, "architecture-exceptions.json", e)
			err := check(context.Background(), root, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), io.Discard)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}
func TestExactExceptionLifecycle(t *testing.T) {
	for _, scenario := range []string{"valid", "expired", "future-created", "bad-date", "terminal-task", "wrong-task-id", "missing-task", "ownerless", "unjustified", "wildcard", "unknown-rule", "duplicate", "orphan", "insufficient-ceiling", "unused"} {
		t.Run(scenario, func(t *testing.T) {
			root, c, es := fixturePolicy(t)
			put(t, root, "records.go", "package records\n\n\n\n")
			e := exception{Rule: "source-budget", Path: "records.go", Owner: "operator", Reason: "bounded refactor", Created: "2026-10-23", Expires: "2026-11-01", TaskID: "remove-limit", Ceiling: 4}
			switch scenario {
			case "expired":
				e.Expires = "2026-10-24"
			case "future-created":
				e.Created = "2026-10-25"
			case "bad-date":
				e.Created = "2026-02-30"
			case "terminal-task":
				put(t, root, "tasks/projects/tsuzuri-go/tasks/remove-limit.task.md", "+++\nid = \"remove-limit\"\nstate = \"done\"\n+++\n")
			case "wrong-task-id":
				put(t, root, "tasks/projects/tsuzuri-go/tasks/remove-limit.task.md", "+++\nid = \"another\"\nstate = \"ready\"\n+++\n")
			case "missing-task":
				e.TaskID = "missing"
			case "ownerless":
				e.Owner = ""
			case "unjustified":
				e.Reason = ""
			case "wildcard":
				e.Path = "*.go"
			case "unknown-rule":
				e.Rule = "ignore"
			case "orphan":
				e.Path = "missing.go"
			case "insufficient-ceiling":
				e.Ceiling = 3
			case "unused":
				put(t, root, "records.go", "package records\n")
			}
			es.Exceptions = []exception{e}
			if scenario == "duplicate" {
				es.Exceptions = append(es.Exceptions, e)
			}
			save(t, root, "architecture.json", c)
			save(t, root, "architecture-exceptions.json", es)
			err := check(context.Background(), root, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), io.Discard)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid exception")
			}
		})
	}
}
func TestMalformedPolicyAndCancellation(t *testing.T) {
	for _, body := range []string{`{"schema":1,"typo":true}`, `{} {}`} {
		root := t.TempDir()
		put(t, root, "architecture.json", body)
		if err := check(context.Background(), root, time.Now(), io.Discard); err == nil {
			t.Fatal("accepted malformed policy")
		}
	}
	root, c, e := fixturePolicy(t)
	save(t, root, "architecture.json", c)
	save(t, root, "architecture-exceptions.json", e)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := check(ctx, root, time.Now(), io.Discard); err != context.Canceled {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestAreaDependencyCycle(t *testing.T) {
	if err := checkCycles([]area{{Name: "records", DependsOn: []string{"storage"}}, {Name: "storage", DependsOn: []string{"records"}}}); err == nil {
		t.Fatal("accepted cyclic ownership")
	}
	if err := checkCycles([]area{{Name: "records"}, {Name: "storage", DependsOn: []string{"records"}}}); err != nil {
		t.Fatal(err)
	}
}
