package tsuzuri

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type OverviewClaim struct {
	Status     string              `json:"status"`
	Owner      *string             `json:"owner"`
	ClaimedAt  *string             `json:"claimedAt"`
	Assignment *OverviewAssignment `json:"assignment"`
	RefName    string              `json:"refName"`
	ObjectID   string              `json:"objectId"`
	Drift      []string            `json:"drift"`
	Error      *string             `json:"error"`
}

type OverviewAssignment struct {
	Status    string  `json:"status"`
	Worker    *string `json:"worker"`
	UpdatedAt *string `json:"updatedAt"`
	RefName   string  `json:"refName"`
	ObjectID  string  `json:"objectId"`
	Valid     bool    `json:"valid"`
	Error     *string `json:"error"`
}

type OverviewTask struct {
	ID                     string         `json:"id"`
	Title                  string         `json:"title"`
	Project                string         `json:"project"`
	Kind                   string         `json:"kind"`
	CanonicalState         string         `json:"canonicalState"`
	Priority               string         `json:"priority"`
	Created                string         `json:"created"`
	ProjectAvailability    string         `json:"projectAvailability"`
	DependencyStatus       string         `json:"dependencyStatus"`
	UnfinishedDependencies []string       `json:"unfinishedDependencies"`
	CancelledDependencies  []string       `json:"cancelledDependencies"`
	BlockedReason          *string        `json:"blockedReason"`
	Claim                  *OverviewClaim `json:"claim"`
}

type OverviewIssue struct {
	TaskID string `json:"taskId"`
	OverviewClaim
}

var overviewKeys = []string{"claimableNow", "activeClaims", "waitingOnDependencies", "backlog", "blocked", "unavailable", "done", "cancelled"}
var overviewTitles = []string{"Claimable now", "Active claims", "Waiting on dependencies", "Backlog", "Blocked", "Unavailable", "Done", "Cancelled"}

func overviewClaim(c ClaimObservation) OverviewClaim {
	v := OverviewClaim{Status: c.Status, RefName: c.RefName, ObjectID: c.ObjectID, Drift: c.Drift, Error: c.Error}
	if c.Metadata != nil {
		v.Owner = &c.Metadata.Owner
		v.ClaimedAt = &c.Metadata.ClaimedAt
	}
	if a := c.Assignment; a != nil {
		v.Assignment = &OverviewAssignment{Status: a.Status, RefName: a.RefName, ObjectID: a.ObjectID, Valid: a.Valid, Error: a.Error}
		if a.Metadata != nil {
			v.Assignment.Worker = a.Metadata.Worker
			v.Assignment.UpdatedAt = &a.Metadata.UpdatedAt
		}
	}
	return v
}
func (r *repository) overview(ctx context.Context, q Query) (Result, error) {
	if q.Format != "" && q.Format != "terminal" && q.Format != "markdown" || q.Color != "" && !slices.Contains([]string{"auto", "always", "never"}, q.Color) {
		return Result{}, failure("usage", "invalid overview format or color")
	}
	s, e := r.snapshot(ctx)
	if e != nil {
		return Result{}, e
	}
	i := s.loaded
	if q.Project != "" {
		if _, ok := i.Projects[q.Project]; !ok {
			return Result{}, failure("usage", "unknown project "+q.Project)
		}
	}
	observations := r.observe(ctx, s)
	byTask := map[string]ClaimObservation{}
	issues := []OverviewIssue{}
	stale := []OverviewIssue{}
	for _, c := range observations {
		byTask[c.TaskID] = c
		if q.Project != "" && i.Tasks[c.TaskID].Project != q.Project {
			continue
		}
		issue := !c.Valid || c.Assignment != nil && !c.Assignment.Valid
		if c.Status == "active" {
			for _, drift := range c.Drift {
				if drift != "compatible-base-advancement" {
					issue = true
				}
			}
		}
		if issue {
			issues = append(issues, OverviewIssue{c.TaskID, overviewClaim(c)})
		}
		if c.Status == "stale-terminal" {
			stale = append(stale, OverviewIssue{c.TaskID, overviewClaim(c)})
		}
	}
	sections := map[string][]OverviewTask{}
	for _, key := range overviewKeys {
		sections[key] = []OverviewTask{}
	}
	total := 0
	for _, id := range sortedKeys(i.Tasks) {
		t := i.Tasks[id]
		if q.Project != "" && t.Project != q.Project {
			continue
		}
		total++
		v := OverviewTask{ID: id, Title: t.Title, Project: t.Project, Kind: t.Kind, CanonicalState: t.State, Priority: t.Priority, Created: t.Created, ProjectAvailability: i.Projects[t.Project].State, DependencyStatus: i.dependency(t), UnfinishedDependencies: []string{}, CancelledDependencies: []string{}, BlockedReason: t.BlockedReason}
		c, has := byTask[id]
		if has {
			converted := overviewClaim(c)
			v.Claim = &converted
		}
		for _, dep := range t.BlockedBy {
			state := i.Tasks[dep].State
			if state == "cancelled" {
				v.CancelledDependencies = append(v.CancelledDependencies, dep)
			} else if state != "done" {
				v.UnfinishedDependencies = append(v.UnfinishedDependencies, dep)
			}
		}
		section := "unavailable"
		switch {
		case terminal(t.State):
			section = t.State
		case has && c.Valid && c.Status == "active":
			section = "activeClaims"
		case has && !c.Valid:
			section = "unavailable"
		case t.State == "backlog" || t.State == "blocked":
			section = t.State
		case i.dependency(t) != "satisfied":
			section = "waitingOnDependencies"
		case i.claimable(t):
			section = "claimableNow"
		}
		sections[section] = append(sections[section], v)
	}
	summary := map[string]int{"totalTasks": total, "claimIssues": len(issues), "staleTerminalClaims": len(stale)}
	data := map[string]any{"project": nullable(q.Project), "summary": summary, "claimIssues": issues, "staleTerminalClaims": stale}
	for _, key := range overviewKeys {
		slices.SortFunc(sections[key], func(a, b OverviewTask) int {
			if n := strings.Compare(a.Priority, b.Priority); n != 0 {
				return n
			}
			if n := strings.Compare(a.Created, b.Created); n != 0 {
				return n
			}
			return strings.Compare(a.ID, b.ID)
		})
		summary[key] = len(sections[key])
		data[key] = sections[key]
	}
	human := renderOverview(q, s, summary, sections, issues, stale)
	return successful("overview", &i.Source, data, human), nil
}
func display(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(display(s), "|", "\\|"), "`", "\\`")
}
func textOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
func overviewColumns(key string, t OverviewTask) ([]string, []string) {
	switch key {
	case "claimableNow":
		return []string{"Priority", "Kind", "Task", "Title"}, []string{t.Priority, t.Kind, t.ID, t.Title}
	case "activeClaims":
		c := t.Claim
		owner, worker, state, updated, drift := "unknown", "-", "unassigned", "-", "-"
		if c != nil {
			owner = textOr(c.Owner, owner)
			if len(c.Drift) > 0 {
				drift = strings.Join(c.Drift, ", ")
			}
			if a := c.Assignment; a != nil {
				worker = textOr(a.Worker, worker)
				state = a.Status
				updated = textOr(a.UpdatedAt, updated)
			}
		}
		return []string{"Priority", "Task", "Owner", "Worker", "Assignment", "Updated", "Drift"}, []string{t.Priority, t.ID, owner, worker, state, updated, drift}
	case "waitingOnDependencies":
		return []string{"Priority", "Task", "Waiting for"}, []string{t.Priority, t.ID, strings.Join(append(append([]string{}, t.UnfinishedDependencies...), t.CancelledDependencies...), ", ")}
	case "blocked":
		return []string{"Priority", "Task", "Reason"}, []string{t.Priority, t.ID, textOr(t.BlockedReason, "reason unavailable")}
	case "unavailable":
		reason := "project " + t.ProjectAvailability
		if t.Claim != nil {
			reason = textOr(t.Claim.Error, "claim unavailable")
		}
		return []string{"Priority", "Task", "Reason"}, []string{t.Priority, t.ID, reason}
	default:
		return []string{"Priority", "Task", "Title"}, []string{t.Priority, t.ID, t.Title}
	}
}
func renderOverview(q Query, s *snapshot, summary map[string]int, sections map[string][]OverviewTask, issues, stale []OverviewIssue) string {
	var out strings.Builder
	markdown := q.Format == "markdown"
	color := q.Color == "always" && !markdown
	paint := func(s, code string) string {
		if color {
			return "\x1b[" + code + "m" + s + "\x1b[0m"
		}
		return s
	}
	title := paint("Tsuzuri overview", "1")
	if markdown {
		title = "# Tsuzuri overview"
	}
	fmt.Fprintf(&out, "%s\n\nProject: %s\nBase: %s\nSnapshot: %s\n", title, textOr(nullable(q.Project), "all projects"), s.base, s.id)
	if markdown {
		fmt.Fprintf(&out, "\n| Claimable | Active | Waiting | Backlog | Blocked | Unavailable | Done | Cancelled |\n|---|---|---|---|---|---|---|---|\n| %d | %d | %d | %d | %d | %d | %d | %d |\n", summary["claimableNow"], summary["activeClaims"], summary["waitingOnDependencies"], summary["backlog"], summary["blocked"], summary["unavailable"], summary["done"], summary["cancelled"])
	} else {
		fmt.Fprintf(&out, "\nclaimable=%d  active=%d  waiting=%d  backlog=%d  blocked=%d  unavailable=%d  done=%d  cancelled=%d  claim_issues=%d  stale_terminal_claims=%d\n", summary["claimableNow"], summary["activeClaims"], summary["waitingOnDependencies"], summary["backlog"], summary["blocked"], summary["unavailable"], summary["done"], summary["cancelled"], len(issues), len(stale))
	}
	table := func(title, code string, headers []string, rows [][]string) {
		if markdown {
			fmt.Fprintf(&out, "\n## %s (%d)\n\n", title, len(rows))
		} else {
			fmt.Fprintf(&out, "\n%s\n", paint(fmt.Sprintf("%s (%d)", title, len(rows)), code))
		}
		if len(rows) == 0 {
			out.WriteString("None.\n")
			return
		}
		widths := make([]int, len(headers))
		for col, h := range headers {
			widths[col] = utf8.RuneCountInString(h)
		}
		for _, row := range rows {
			for j, value := range row {
				if n := utf8.RuneCountInString(display(value)); n > widths[j] {
					widths[j] = n
				}
			}
		}
		emit := func(row []string, header bool) {
			if markdown {
				out.WriteString("| ")
			}
			for col, value := range row {
				value = display(value)
				if markdown {
					value = cell(value)
					if !header && headers[col] == "Task" {
						value = "`" + value + "`"
					}
					out.WriteString(value)
					out.WriteString(" |")
					if col+1 < len(row) {
						out.WriteByte(' ')
					}
				} else {
					padding := widths[col] - utf8.RuneCountInString(value) + 2
					if !header && headers[col] == "Priority" {
						tone := "1;33"
						if value == "p0" {
							tone = "1;31"
						}
						value = paint(value, tone)
					}
					out.WriteString(value)
					if col+1 < len(row) {
						out.WriteString(strings.Repeat(" ", padding))
					}
				}
			}
			out.WriteByte('\n')
		}
		emit(headers, true)
		if markdown {
			out.WriteByte('|')
			for range headers {
				out.WriteString("---|")
			}
			out.WriteByte('\n')
		}
		for _, row := range rows {
			emit(row, false)
		}
	}
	for n, key := range overviewKeys {
		tasks := sections[key]
		headers, _ := overviewColumns(key, OverviewTask{})
		rows := [][]string{}
		for _, t := range tasks {
			_, row := overviewColumns(key, t)
			rows = append(rows, row)
		}
		tones := []string{"1;32", "1;36", "1;33", "1;34", "1;31", "1;31", "2", "2"}
		table(overviewTitles[n], tones[n], headers, rows)
	}
	for _, group := range []struct {
		title  string
		values []OverviewIssue
	}{{"Claim issues", issues}, {"Stale terminal claims", stale}} {
		if len(group.values) == 0 {
			continue
		}
		rows := [][]string{}
		for _, c := range group.values {
			detail := textOr(c.Error, strings.Join(c.Drift, ", "))
			if c.Assignment != nil && c.Assignment.Error != nil {
				detail += " " + *c.Assignment.Error
			}
			rows = append(rows, []string{c.TaskID, c.Status, detail})
		}
		table(group.title, "1;31", []string{"Task", "Status", "Detail"}, rows)
	}
	return strings.TrimSpace(out.String())
}
