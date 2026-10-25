package tsuzuri

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Config is the schema-1 repository configuration stored in .tsuzuri.toml.
type Config struct {
	Schema           int      `toml:"schema"`
	RepositoryID     string   `toml:"repository_id"`
	TaskRoot         string   `toml:"task_root"`
	BaseBranch       string   `toml:"base_branch"`
	ClaimNamespace   string   `toml:"claim_namespace"`
	WorktreeRoot     string   `toml:"worktree_root"`
	WorktreeTemplate string   `toml:"worktree_template"`
	Areas            []string `toml:"areas"`
	Risks            []string `toml:"risks"`
}

type Project struct {
	Schema     int            `json:"schema" toml:"schema"`
	ID         string         `json:"id" toml:"id"`
	Title      string         `json:"title" toml:"title"`
	State      string         `json:"state" toml:"state"`
	Goal       string         `json:"goal" toml:"-"`
	FinishLine string         `json:"finishLine" toml:"-"`
	Path       string         `json:"path" toml:"-"`
	Extensions map[string]any `json:"-" toml:"extensions"`
}

// Task is a parsed task record; its state is canonical only on the base branch.
type Task struct {
	Schema        int            `json:"schema" toml:"schema"`
	ID            string         `json:"id" toml:"id"`
	Project       string         `json:"project" toml:"project"`
	Title         string         `json:"title" toml:"title"`
	Kind          string         `json:"kind" toml:"kind"`
	State         string         `json:"state" toml:"state"`
	Priority      string         `json:"priority" toml:"priority"`
	BlockedBy     []string       `json:"blockedBy" toml:"blocked_by"`
	Areas         []string       `json:"areas" toml:"areas"`
	Risks         []string       `json:"risks" toml:"risks"`
	Created       string         `json:"created" toml:"-"`
	CreatedDate   toml.LocalDate `json:"-" toml:"created"`
	BlockedReason *string        `json:"blockedReason" toml:"blocked_reason"`
	Path          string         `json:"path" toml:"-"`
	Extensions    map[string]any `json:"-" toml:"extensions"`
}
type inventory struct {
	Config    Config
	Projects  map[string]Project
	Tasks     map[string]Task
	Documents map[string]string
	Digests   map[string]string
	Source    Source
}

// Source identifies the repository revision and refs used for an observation.
type Source struct {
	Kind                        string   `json:"kind"`
	RepositoryID                string   `json:"repositoryId"`
	BaseObjectID                *string  `json:"baseObjectId"`
	ConfigurationObjectID       *string  `json:"configurationObjectId"`
	SnapshotID                  *string  `json:"snapshotId"`
	ObservedClaimObjectIDs      []string `json:"observedClaimObjectIds"`
	ObservedAssignmentObjectIDs []string `json:"observedAssignmentObjectIds"`
	RefreshResult               *string  `json:"refreshResult"`
}

func source(kind, id string) Source {
	return Source{Kind: kind, RepositoryID: id, ObservedClaimObjectIDs: []string{}, ObservedAssignmentObjectIDs: []string{}}
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func validID(s string) bool  { return idPattern.MatchString(s) }
func terminal(s string) bool { return s == "done" || s == "cancelled" }
func ordered(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] >= s[i] {
			return false
		}
	}
	return true
}
func decodeTOML(s string, v any) error {
	if e := toml.NewDecoder(strings.NewReader(s)).DisallowUnknownFields().Decode(v); e != nil {
		return invalid("invalid TOML: %v", e).causedBy(e)
	}
	return nil
}
func parseConfig(s string) (Config, error) {
	var c Config
	if e := decodeTOML(s, &c); e != nil {
		return c, e
	}
	if c.Schema != 1 {
		return c, failure("incompatible-schema", "unsupported configuration schema")
	}
	if !uuidPattern.MatchString(c.RepositoryID) || c.BaseBranch == "" || c.WorktreeRoot == "" || !normalized(c.TaskRoot) || strings.HasPrefix(c.ClaimNamespace, "refs/") || strings.HasPrefix(c.ClaimNamespace, "/") || strings.HasSuffix(c.ClaimNamespace, "/") || c.ClaimNamespace == "" || !strings.Contains(c.WorktreeTemplate, "{task_id}") || c.Areas == nil || c.Risks == nil {
		return c, invalid("invalid or incomplete repository configuration")
	}
	return c, nil
}
func normalized(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}
func document(s string) (string, string, error) {
	if len(s) > maxRecord || strings.Contains(s, "\r") || !strings.HasPrefix(s, "+++\n") {
		return "", "", invalid("record must start with +++ and use LF newlines")
	}
	front, body, ok := strings.Cut(s[4:], "\n+++\n")
	if !ok {
		return "", "", invalid("missing closing +++ delimiter")
	}
	return front, body, nil
}
func recordCheck(schema int, id, title string, ext map[string]any) error {
	if schema != 1 || !validID(id) || strings.TrimSpace(title) == "" || strings.Contains(title, "\n") {
		return invalid("invalid record schema, identity or title")
	}
	for k, v := range ext {
		if strings.TrimSpace(k) == "" {
			return invalid("empty extension namespace")
		}
		if _, ok := v.(map[string]any); !ok {
			return invalid("extensions must contain namespaced tables")
		}
	}
	return nil
}
func sectionContent(body, name string) string {
	found := false
	var lines []string
	for _, s := range strings.Split(body, "\n") {
		if found {
			if strings.HasPrefix(s, "# ") {
				break
			}
			lines = append(lines, s)
		} else if s == "# "+name {
			found = true
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
func sections(body string, names ...string) error {
	src := []byte(body)
	root := goldmark.DefaultParser().Parse(text.NewReader(src))
	counts := map[string]int{}
	content := map[string]bool{}
	current := ""
	heading := false
	err := ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok {
			if h.Level == 1 {
				if enter {
					current = strings.TrimSpace(string(h.Text(src)))
					counts[current]++
					content[current] = false
					heading = true
				} else {
					heading = false
				}
			}
			return ast.WalkContinue, nil
		}
		if !enter || heading || current == "" {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindText, ast.KindString, ast.KindRawHTML, ast.KindHTMLBlock, ast.KindCodeBlock, ast.KindFencedCodeBlock:
			if strings.TrimSpace(string(n.Text(src))) != "" {
				content[current] = true
			}
			if t, ok := n.(*ast.Text); ok && (t.SoftLineBreak() || t.HardLineBreak()) {
				content[current] = true
			}
		case ast.KindThematicBreak:
			content[current] = true
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return invalid("Markdown parse: %v", err).causedBy(err)
	}
	for _, name := range names {
		if counts[name] != 1 || !content[name] || !slices.Contains(strings.Split(body, "\n"), "# "+name) {
			return invalid("# %s must appear exactly once as an exact nonempty top-level section", name)
		}
	}
	return nil
}
func parseProject(s, path string) (Project, error) {
	var p Project
	front, body, e := document(s)
	if e != nil {
		return p, e
	}
	if e = decodeTOML(front, &p); e != nil {
		return p, e
	}
	if e = recordCheck(p.Schema, p.ID, p.Title, p.Extensions); e != nil {
		return p, e
	}
	if !slices.Contains([]string{"active", "paused", "completed"}, p.State) {
		return p, invalid("invalid project state")
	}
	if e = sections(body, "Goal", "Finish line"); e != nil {
		return p, e
	}
	p.Path = path
	p.Goal = sectionContent(body, "Goal")
	p.FinishLine = sectionContent(body, "Finish line")
	return p, nil
}
func parseTask(s, path string, legacy bool) (Task, error) {
	t, body, e := parseTaskFields(s, legacy)
	if e != nil {
		return t, e
	}
	if terminal(t.State) {
		e = sections(body, "Resolution")
	} else {
		e = sections(body, "Outcome", "Acceptance")
	}
	if e != nil {
		return t, e
	}
	t.Path = path
	return t, nil
}

// Historical terminal prose may be corrected later; current and claimed trees
// always use parseTask. Nonterminal acceptance remains strict in every revision.
func parseHistoricalTask(s string) (Task, error) {
	t, body, e := parseTaskFields(s, true)
	if e != nil || terminal(t.State) {
		return t, e
	}
	return t, sections(body, "Outcome", "Acceptance")
}

func parseTaskFields(s string, legacy bool) (Task, string, error) {
	var t Task
	front, body, e := document(s)
	if e != nil {
		return t, "", e
	}
	if e = decodeTOML(front, &t); e != nil {
		return t, "", e
	}
	if e = recordCheck(t.Schema, t.ID, t.Title, t.Extensions); e != nil {
		return t, "", e
	}
	if t.Kind == "" && legacy {
		t.Kind = "legacy-unclassified"
	} else if !slices.Contains([]string{"delivery", "defect", "maintenance", "tech-debt", "operations", "research"}, t.Kind) {
		return t, "", invalid("kind is required and must be a supported task kind")
	}
	if !validID(t.Project) || !slices.Contains([]string{"backlog", "ready", "blocked", "done", "cancelled"}, t.State) || !slices.Contains([]string{"p0", "p1", "p2", "p3"}, t.Priority) {
		return t, "", invalid("invalid task project, state or priority")
	}
	if t.BlockedBy == nil || t.Areas == nil || t.Risks == nil || !ordered(t.BlockedBy) || !ordered(t.Areas) || !ordered(t.Risks) {
		return t, "", invalid("task registries must be supplied, sorted and unique")
	}
	t.Created = t.CreatedDate.String()
	if _, e = time.Parse("2006-01-02", t.Created); e != nil {
		return t, "", invalid("created must be a TOML local date")
	}
	if t.State == "blocked" {
		if t.BlockedReason == nil || strings.TrimSpace(*t.BlockedReason) == "" {
			return t, "", invalid("blocked_reason required")
		}
	} else if t.BlockedReason != nil {
		return t, "", invalid("blocked_reason only allowed for blocked tasks")
	}
	return t, body, nil
}
func (i *inventory) validate() error {
	for _, r := range [][]string{i.Config.Areas, i.Config.Risks} {
		if !ordered(r) {
			return invalid("configuration registries must be sorted and unique")
		}
		for _, s := range r {
			if !validID(s) {
				return invalid("invalid registry value")
			}
		}
	}
	for _, t := range i.Tasks {
		p, ok := i.Projects[t.Project]
		if !ok {
			return invalid("task %s references missing project", t.ID)
		}
		if p.State == "completed" && !terminal(t.State) {
			return invalid("completed project contains nonterminal task")
		}
		for _, d := range t.BlockedBy {
			if _, ok := i.Tasks[d]; !ok || d == t.ID {
				return invalid("task %s has invalid dependency %s", t.ID, d)
			}
		}
		for _, a := range t.Areas {
			if !slices.Contains(i.Config.Areas, a) {
				return invalid("unregistered area %s", a)
			}
		}
		for _, r := range t.Risks {
			if !slices.Contains(i.Config.Risks, r) {
				return invalid("unregistered risk %s", r)
			}
		}
	}
	// Iterative DFS avoids process-stack growth on valid deep dependency graphs.
	state := map[string]uint8{}
	type step struct {
		id   string
		exit bool
	}
	for id := range i.Tasks {
		stack := []step{{id, false}}
		for len(stack) > 0 {
			s := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if s.exit {
				state[s.id] = 2
				continue
			}
			if state[s.id] == 2 {
				continue
			}
			if state[s.id] == 1 {
				return invalid("dependency cycle includes %s", s.id)
			}
			state[s.id] = 1
			stack = append(stack, step{s.id, true})
			for _, d := range i.Tasks[s.id].BlockedBy {
				stack = append(stack, step{d, false})
			}
		}
	}
	return nil
}
func (i *inventory) dependency(t Task) string {
	state := "satisfied"
	for _, id := range t.BlockedBy {
		d, ok := i.Tasks[id]
		if ok && d.State == "cancelled" {
			return "cancelled"
		}
		if !ok || d.State != "done" {
			state = "unfinished"
		}
	}
	return state
}
func (i *inventory) claimable(t Task) bool {
	return t.State == "ready" && i.dependency(t) == "satisfied" && i.Projects[t.Project].State == "active"
}
func (i *inventory) add(path, s, digest string, legacy bool) error {
	if strings.HasSuffix(path, "/project.md") {
		p, e := parseProject(s, path)
		if e != nil {
			return e
		}
		return i.addProject(p, path, s, digest)
	} else if strings.HasSuffix(path, ".task.md") {
		t, e := parseTask(s, path, legacy)
		if e != nil {
			return e
		}
		return i.addTask(t, path, s, digest)
	}
	return nil
}
func (i *inventory) addProject(p Project, path, s, digest string) error {
	if path != i.Config.TaskRoot+"/"+p.ID+"/project.md" {
		return invalid("invalid project path %s", path)
	}
	if _, ok := i.Projects[p.ID]; ok {
		return invalid("duplicate project %s", p.ID)
	}
	p.Path = path
	i.Projects[p.ID] = p
	i.Documents[path] = s
	i.Digests[path] = digest
	return nil
}
func (i *inventory) addTask(t Task, path, s, digest string) error {
	if path != fmt.Sprintf("%s/%s/tasks/%s.task.md", i.Config.TaskRoot, t.Project, t.ID) {
		return invalid("invalid task path %s", path)
	}
	if _, ok := i.Tasks[t.ID]; ok {
		return invalid("duplicate task %s", t.ID)
	}
	t.Path = path
	i.Tasks[t.ID] = t
	i.Documents[path] = s
	i.Digests[path] = digest
	return nil
}
