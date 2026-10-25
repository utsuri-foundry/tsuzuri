package tsuzuri

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const Version = "0.1.0-rc.5"

// Result is the schema-1 automation envelope. Human output is a rendering only.
type Result struct {
	Schema  int     `json:"schema"`
	OK      bool    `json:"ok"`
	Command string  `json:"command"`
	Source  *Source `json:"source"`
	Data    any     `json:"data"`
	Human   string  `json:"-"`
}

type Query struct {
	Command, ID, Source, Project, Kind, State, ClaimStatus, Format, Color string
	Refresh                                                               bool
}

type TaskView struct {
	Task
	ProjectAvailability  string  `json:"projectAvailability"`
	DependencyStatus     string  `json:"dependencyStatus"`
	ClaimStatus          string  `json:"claimStatus"`
	Owner                *string `json:"owner"`
	Worker               *string `json:"worker"`
	AssignmentStatus     string  `json:"assignmentStatus"`
	CanonicallyClaimable bool    `json:"canonicallyClaimable"`
	RecordDigest         string  `json:"recordDigest"`
}

// Inspection includes current task text and the original claimed revision.
type Inspection struct {
	TaskView
	Markdown        string            `json:"markdown"`
	Claim           *ClaimObservation `json:"claim"`
	ClaimedMarkdown *string           `json:"claimedMarkdown"`
}

func successful(command string, source *Source, data any, human string) Result {
	return Result{1, true, command, source, data, human}
}

// Show inspects a task and its current ownership on a fresh canonical snapshot.
func (c *Client) Show(ctx context.Context, id string, options ...OperationOptions) (Inspection, Source, error) {
	result, e := c.Query(ctx, Query{Command: "show", ID: id}, options...)
	if e != nil {
		return Inspection{}, Source{}, e
	}
	return result.Data.(Inspection), *result.Source, nil
}

// Query returns a schema-1 envelope for the requested inspection.
func (c *Client) Query(ctx context.Context, q Query, options ...OperationOptions) (Result, error) {
	if q.Command == "compatibility" {
		return compatibility(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	r, e := c.session(ctx, options...)
	if e != nil {
		return Result{}, e
	}
	result, e := r.query(ctx, q)
	// Inspection deliberately represents invalid individual claims as data.
	// Cancellation must still fail the operation, not become a successful partial
	// inventory after an interrupted ownership observation.
	if e == nil && ctx.Err() != nil {
		e = invalid("Git operation cancelled or timed out: %v", ctx.Err()).causedBy(ctx.Err())
	}
	return result, e
}
func compatibility() Result {
	return successful("compatibility", nil, map[string]any{"cliVersion": Version, "jsonSchemaVersions": []int{1}, "repositorySchemaVersions": []int{1}, "claimProtocolVersions": []int{1}, "assignmentProtocolVersions": []int{1}, "nonInteractive": true, "responseEnvelopeStream": "stdout", "capabilities": []string{"claim-no-worktree", "accept-no-worktree", "claim-expected-digest", "claimed-task-revision", "show-task-content", "handoff-checkpoint"}, "commands": []string{"accept", "claim", "compatibility", "create-project", "create-task", "doctor", "handoff", "list", "materialize", "next", "overview", "project", "projects", "release", "show", "status", "transition-task", "validate"}}, "Tsuzuri "+Version+" (JSON schema 1)")
}
func (r *repository) query(ctx context.Context, q Query) (Result, error) {
	if q.Command == "overview" {
		return r.overview(ctx, q)
	}
	if q.Command == "status" || q.Command == "doctor" {
		return r.status(ctx, q)
	}
	if q.Source != "" && q.Source != "base" && q.Source != "worktree" {
		return Result{}, failure("usage", "unknown source")
	}
	if q.Kind != "" && !slices.Contains([]string{"delivery", "defect", "maintenance", "tech-debt", "operations", "research"}, q.Kind) {
		return Result{}, failure("usage", "unknown task kind `"+q.Kind+"`")
	}
	if q.State != "" && !slices.Contains([]string{"backlog", "ready", "blocked", "done", "cancelled"}, q.State) {
		return Result{}, failure("usage", "unknown state")
	}
	if q.ClaimStatus != "" && !slices.Contains([]string{"absent", "active", "invalid", "stale-terminal"}, q.ClaimStatus) {
		return Result{}, failure("usage", "unknown claim status")
	}
	var i *inventory
	var observed []ClaimObservation
	var e error
	if q.Command == "validate" {
		if q.Source == "base" {
			cfg, err := r.config(ctx)
			if err != nil {
				return Result{}, err
			}
			i, e = r.tree(ctx, cfg.BaseBranch, false, true)
		} else {
			i, e = r.worktree(ctx)
		}
		if e != nil {
			return Result{}, e
		}
		return successful(q.Command, &i.Source, map[string]any{"projects": len(i.Projects), "tasks": len(i.Tasks)}, "Repository records are valid."), nil
	}
	if q.Source == "worktree" && !q.Refresh && (q.Command == "show" || q.Command == "project" || q.Command == "projects") {
		i, e = r.worktree(ctx)
	} else {
		var s *snapshot
		s, e = r.snapshot(ctx)
		if e == nil {
			i = s.loaded
			observed = r.observe(ctx, s)
		}
	}
	if e != nil {
		return Result{}, e
	}
	switch q.Command {
	case "show":
		task, ok := i.Tasks[q.ID]
		if !ok {
			return Result{}, failure("usage", "unknown task "+q.ID)
		}
		claim := findClaim(observed, q.ID)
		view := viewTask(i, task, claim)
		inspection := Inspection{TaskView: view, Markdown: i.Documents[task.Path], Claim: claim}
		if claim != nil && claim.ClaimedTask != nil {
			original, err := r.show(ctx, claim.ClaimedTask.BaseObjectID, claim.ClaimedTask.Path)
			if err != nil {
				return Result{}, err
			}
			inspection.ClaimedMarkdown = &original
		}
		return successful(q.Command, &i.Source, inspection, fmt.Sprintf("%s [%s/%s] %s\nproject: %s\ndependencies: %s\nclaim: %s", task.ID, task.Kind, task.State, task.Title, task.Project, view.DependencyStatus, view.ClaimStatus)), nil
	case "projects", "project":
		type projectView struct {
			Project
			RecordDigest string `json:"recordDigest"`
		}
		records := []projectView{}
		for _, id := range sortedKeys(i.Projects) {
			p := i.Projects[id]
			if q.Command == "project" && p.ID != q.ID {
				continue
			}
			records = append(records, projectView{p, i.Digests[p.Path]})
		}
		if q.Command == "project" {
			if len(records) == 0 {
				return Result{}, failure("usage", "unknown project "+q.ID)
			}
			p := records[0]
			return successful(q.Command, &i.Source, p, p.ID+" ["+p.State+"] "+p.Title+"\n\nGoal\n"+p.Goal+"\n\nFinish line\n"+p.FinishLine), nil
		}
		lines := []string{}
		for _, p := range records {
			lines = append(lines, p.State+"\t"+p.ID+"\t"+p.Title)
		}
		return successful(q.Command, &i.Source, records, strings.Join(lines, "\n")), nil
	case "list", "next":
		views := []TaskView{}
		for _, id := range sortedKeys(i.Tasks) {
			t := i.Tasks[id]
			claim := findClaim(observed, id)
			view := viewTask(i, t, claim)
			if q.Project != "" && q.Project != t.Project || q.Kind != "" && q.Kind != t.Kind || q.State != "" && q.State != t.State || q.ClaimStatus != "" && q.ClaimStatus != view.ClaimStatus {
				continue
			}
			if q.Command == "next" && (!view.CanonicallyClaimable || claim != nil) {
				continue
			}
			views = append(views, view)
		}
		if q.Command == "next" {
			slices.SortFunc(views, func(a, b TaskView) int {
				if c := strings.Compare(a.Priority, b.Priority); c != 0 {
					return c
				}
				if c := strings.Compare(a.Created, b.Created); c != 0 {
					return c
				}
				return strings.Compare(a.ID, b.ID)
			})
		}
		lines := []string{}
		for _, v := range views {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s", v.Priority, v.Kind, v.State, v.ID, v.DependencyStatus))
		}
		return successful(q.Command, &i.Source, views, strings.Join(lines, "\n")), nil
	default:
		return Result{}, failure("usage", "unknown query command")
	}
}
func findClaim(observed []ClaimObservation, id string) *ClaimObservation {
	for idx := range observed {
		if observed[idx].TaskID == id {
			return &observed[idx]
		}
	}
	return nil
}
func viewTask(i *inventory, t Task, c *ClaimObservation) TaskView {
	v := TaskView{Task: t, ProjectAvailability: i.Projects[t.Project].State, DependencyStatus: i.dependency(t), ClaimStatus: "absent", AssignmentStatus: "unassigned", CanonicallyClaimable: i.claimable(t), RecordDigest: i.Digests[t.Path]}
	if c != nil {
		v.ClaimStatus = c.Status
		if c.Metadata != nil {
			v.Owner = &c.Metadata.Owner
		}
		if c.Assignment != nil {
			v.AssignmentStatus = c.Assignment.Status
			if c.Assignment.Metadata != nil {
				v.Worker = c.Assignment.Metadata.Worker
			}
		}
	}
	return v
}

// JSON renders exactly one envelope. Native clients can use typed method results.
func JSON(result Result) ([]byte, error) { return json.MarshalIndent(result, "", "  ") }
