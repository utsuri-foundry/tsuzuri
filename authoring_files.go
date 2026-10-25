package tsuzuri

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func renderFront(table map[string]any) (string, error) {
	var b bytes.Buffer
	enc := toml.NewEncoder(&b).SetMarshalJSONNumbers(true)
	if e := enc.Encode(table); e != nil {
		return "", invalid("cannot render front matter: %v", e).causedBy(e)
	}
	return releasedStringSpelling(b.String())
}
func appendSection(body, name, value string) string {
	return body + "\n# " + name + "\n\n" + strings.TrimSpace(value) + "\n"
}
func renderProject(p CreateProjectInput) (string, error) {
	table := map[string]any{"schema": 1, "id": p.ID, "title": p.Title, "state": p.State}
	if len(p.Extensions) > 0 {
		table["extensions"] = p.Extensions
	}
	front, e := renderFront(table)
	if e != nil {
		return "", e
	}
	return "+++\n" + front + "+++\n" + appendSection(appendSection("", "Goal", p.Goal), "Finish line", p.FinishLine), nil
}
func renderTask(t CreateTaskInput) (string, error) {
	date, e := time.Parse("2006-01-02", t.Created)
	if e != nil || date.Format("2006-01-02") != t.Created {
		return "", invalid("created must be a TOML local date")
	}
	if t.BlockedBy == nil || t.Areas == nil || t.Risks == nil {
		return "", invalid("blockedBy, areas, and risks arrays are required")
	}
	table := map[string]any{"schema": 1, "id": t.ID, "project": t.Project, "title": t.Title, "kind": t.Kind, "state": t.State, "priority": t.Priority, "blocked_by": t.BlockedBy, "areas": t.Areas, "risks": t.Risks, "created": toml.LocalDate{Year: date.Year(), Month: int(date.Month()), Day: date.Day()}}
	if t.BlockedReason != nil {
		table["blocked_reason"] = *t.BlockedReason
	}
	if len(t.Extensions) > 0 {
		table["extensions"] = t.Extensions
	}
	front, e := renderFront(table)
	if e != nil {
		return "", e
	}
	body := ""
	for _, pair := range []struct {
		name  string
		value *string
	}{{"Outcome", t.Outcome}, {"Acceptance", t.Acceptance}, {"Resolution", t.Resolution}} {
		if pair.value != nil {
			body = appendSection(body, pair.name, *pair.value)
		}
	}
	for _, name := range sortedKeys(t.AdditionalSections) {
		if name == "Outcome" || name == "Acceptance" || name == "Resolution" {
			return "", invalid("additional section duplicates typed section")
		}
		body = appendSection(body, name, t.AdditionalSections[name])
	}
	return "+++\n" + front + "+++\n" + body, nil
}
func transitionDocument(current string, t TransitionTaskInput) (string, error) {
	front, body, e := document(current)
	if e != nil {
		return "", e
	}
	table := map[string]any{}
	if e = toml.Unmarshal([]byte(front), &table); e != nil {
		return "", invalid("invalid front matter: %v", e).causedBy(e)
	}
	table["state"] = t.State
	if t.State == "blocked" {
		if t.BlockedReason == nil || strings.TrimSpace(*t.BlockedReason) == "" {
			return "", invalid("blocked_reason required when transitioning to blocked")
		}
		table["blocked_reason"] = *t.BlockedReason
	} else {
		if t.BlockedReason != nil {
			return "", invalid("blocked_reason only allowed when transitioning to blocked")
		}
		delete(table, "blocked_reason")
	}
	if terminal(t.State) {
		// Preserve the established transition section rule. Full task validation runs
		// before writing, including CommonMark section validation.
		resolution := ""
		if _, rest, ok := strings.Cut(body, "# Resolution"); ok {
			v, _, _ := strings.Cut(rest, "\n# ")
			resolution = strings.TrimSpace(v)
		}
		if resolution != "" {
			if t.Resolution != nil && strings.TrimSpace(*t.Resolution) != resolution {
				return "", failure("stale-input", "provided Resolution differs from existing section")
			}
		} else {
			if t.Resolution == nil || strings.TrimSpace(*t.Resolution) == "" {
				return "", invalid("nonempty Resolution required for terminal transition")
			}
			body = appendSection(body, "Resolution", *t.Resolution)
		}
	} else if t.Resolution != nil {
		return "", invalid("resolution only allowed for terminal transition")
	}
	rendered, e := renderFront(table)
	if e != nil {
		return "", e
	}
	return "+++\n" + rendered + "+++\n" + body, nil
}
func (r *repository) transitionLock(ctx context.Context, cfg Config, path string) (string, error) {
	digest, e := r.blob(ctx, path, false)
	return "refs/tsuzuri/locks/" + cfg.RepositoryID + "/" + digest, e
}
func (r *repository) unlockTransition(ctx context.Context, cfg Config, path, owner string) error {
	ref, e := r.transitionLock(ctx, cfg, path)
	if e != nil {
		return e
	}
	old, e := r.ref(ctx, ref)
	if e != nil {
		return e
	}
	if old == "" {
		return nil
	}
	if old != owner {
		return failure("stale-input", "transition lock changed ownership")
	}
	return r.remove(ctx, ref, owner)
}
func (r *repository) lockTransition(ctx context.Context, cfg Config, path, owner string) error {
	ref, e := r.transitionLock(ctx, cfg, path)
	if e != nil {
		return e
	}
	old, e := r.ref(ctx, ref)
	if e != nil {
		return e
	}
	if old == owner {
		return nil
	}
	if old != "" {
		return failure("stale-input", "another operation owns transition lock")
	}
	if e = r.update(ctx, ref, owner, ""); e != nil {
		actual, readErr := r.ref(ctx, ref)
		if readErr != nil {
			return readErr
		}
		if actual != owner {
			return failure("stale-input", "another operation concurrently acquired transition lock")
		}
	}
	return nil
}
func (r *repository) writeRecord(ctx context.Context, cfg Config, path, desired string, expected *string, owner string) (err error) {
	if len(desired) > maxRecord {
		return invalid("record exceeds size bound")
	}
	parent := filepath.Dir(path)
	// Check each existing component before mkdir so a pre-existing symlink never
	// causes even directory creation outside the selected checkout.
	rel, e := filepath.Rel(r.root, parent)
	if e != nil || !normalized(rel) {
		return invalid("record parent outside checkout")
	}
	part := r.root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		part = filepath.Join(part, name)
		st, e := os.Lstat(part)
		if os.IsNotExist(e) {
			if e = os.Mkdir(part, 0755); e != nil && !os.IsExist(e) {
				return invalid("cannot create record parent: %v", e).causedBy(e)
			}
			st, e = os.Lstat(part)
		}
		if e != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return invalid("record parent must contain only physical directories")
		}
	}
	if e = physicalPath(r.root, parent); e != nil {
		return e
	}
	if expected != nil {
		if e = r.lockTransition(ctx, cfg, path, owner); e != nil {
			return e
		}
		defer func() {
			if unlock := r.unlockTransition(ctx, cfg, path, owner); unlock != nil {
				if err == nil {
					err = unlock
				} else {
					err = invalid("%v; lock cleanup: %v", err, unlock).causedBy(errors.Join(err, unlock))
				}
			}
		}()
		current, e := regularFile(path)
		if e != nil {
			return e
		}
		actual, e := r.blob(ctx, current, false)
		if e != nil {
			return e
		}
		if actual != *expected {
			return failure("stale-input", "record changed before replacement")
		}
	}
	f, e := os.CreateTemp(parent, ".tsuzuri-*")
	if e != nil {
		return invalid("cannot create temporary record: %v", e).causedBy(e)
	}
	temp := f.Name()
	defer func() {
		if e := os.Remove(temp); e != nil && !os.IsNotExist(e) {
			err = errors.Join(err, failure("preserved-work", fmt.Sprintf("temporary record could not be removed: %v", e)).causedBy(e))
		}
	}()
	if e = f.Chmod(0644); e == nil {
		_, e = f.WriteString(desired)
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return invalid("cannot write record: %v", e).causedBy(e)
	}
	if expected == nil {
		if e = os.Link(temp, path); e != nil {
			return failure("idempotency-conflict", "record destination already exists or cannot be linked").causedBy(e)
		}
	} else {
		if e = os.Rename(temp, path); e != nil {
			return invalid("could not replace record: %v", e).causedBy(e)
		}
	}
	return nil
}
