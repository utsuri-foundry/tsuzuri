package tsuzuri

import (
	"context"
	"strconv"
	"strings"
)

const historyRevisionBatch = 128
const historyObjectBatch = 512
const historyBlobBatchBytes = 8 << 20

// These describe raw first-parent changes, not rename heuristics. A whole commit
// is applied before checking task identities, including delete/add moves.
type historyChange struct{ path, mode, object string }
type historyDelta struct {
	commit  string
	changes []historyChange
}

func parseHistoryDiff(out string, commits []string) ([]historyDelta, error) {
	result := make([]historyDelta, 0, len(commits))
	for _, commit := range commits {
		token, rest, ok := strings.Cut(out, "\x00")
		if !ok || token != commit {
			return nil, invalid("missing or unordered history commit")
		}
		out = rest
		d := historyDelta{commit: commit}
		for strings.HasPrefix(out, ":") {
			header, rest, ok := strings.Cut(out, "\x00")
			if !ok {
				return nil, invalid("truncated history diff header")
			}
			path, tail, ok := strings.Cut(rest, "\x00")
			if !ok || path == "" {
				return nil, invalid("truncated history diff path")
			}
			f := strings.Fields(header[1:])
			if len(f) != 5 || !validObject(f[2]) || !validObject(f[3]) || !strings.Contains("AMDT", f[4]) || len(f[4]) != 1 {
				return nil, invalid("invalid history diff header")
			}
			for _, mode := range f[:2] {
				if len(mode) != 6 {
					return nil, invalid("invalid history mode")
				}
				if _, e := strconv.ParseUint(mode, 8, 32); e != nil {
					return nil, invalid("invalid history mode")
				}
			}
			if (f[4] == "D") != (f[1] == "000000") || (f[4] == "A") != (f[0] == "000000") {
				return nil, invalid("inconsistent history diff modes")
			}
			if (f[1] == "000000") != (strings.Trim(f[3], "0") == "") || (f[0] == "000000") != (strings.Trim(f[2], "0") == "") {
				return nil, invalid("inconsistent history diff objects")
			}
			d.changes = append(d.changes, historyChange{path, f[1], f[3]})
			if len(d.changes) > maxRecords*2 {
				return nil, invalid("too many changes in history revision")
			}
			out = tail
		}
		result = append(result, d)
	}
	if out != "" {
		return nil, invalid("trailing history diff output")
	}
	return result, nil
}

func (r *repository) historyDiff(ctx context.Context, commits, paths []string) ([]historyDelta, error) {
	for _, id := range commits {
		if !validObject(id) {
			return nil, invalid("invalid history commit")
		}
	}
	args := []string{"diff-tree", "--stdin", "--root", "--always", "-r", "--raw", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", "--diff-merges=first-parent", "-z", "--"}
	args = append(args, paths...)
	out, err := r.input(ctx, strings.Join(commits, "\n")+"\n", nil, args...)
	if err != nil {
		return nil, err
	}
	return parseHistoryDiff(out, commits)
}

// Check sizes before reading content, then read at most 8 MiB per batch. Git's
// framing and actual lengths are verified again by objects. No persistent child,
// unbounded stream, or whole-history blob payload is retained.
func (r *repository) historyBlobs(ctx context.Context, ids []string, visit func(string, string) error) error {
	for start := 0; start < len(ids); start += historyObjectBatch {
		group := ids[start:min(start+historyObjectBatch, len(ids))]
		for _, id := range group {
			if !validObject(id) {
				return invalid("invalid history blob")
			}
		}
		out, err := r.input(ctx, strings.Join(group, "\n")+"\n", nil, "cat-file", "--batch-check")
		if err != nil {
			return err
		}
		sizes, err := historyBlobSizes(out, group)
		if err != nil {
			return err
		}
		for first := 0; first < len(group); {
			last, bytes := first, 0
			for last < len(group) && bytes+sizes[last]+128 <= historyBlobBatchBytes {
				bytes += sizes[last] + 128
				last++
			}
			bodies, err := r.objects(ctx, group[first:last], "blob")
			if err != nil {
				return err
			}
			for j, body := range bodies {
				if err = ctx.Err(); err != nil {
					return invalid("history validation cancelled").causedBy(err)
				}
				if err = visit(group[first+j], body); err != nil {
					return err
				}
			}
			first = last
		}
	}
	return nil
}
func historyBlobSizes(out string, ids []string) ([]int, error) {
	sizes := make([]int, 0, len(ids))
	for _, id := range ids {
		header, rest, ok := strings.Cut(out, "\n")
		if !ok {
			return nil, invalid("truncated history blob header")
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[0] != id || f[1] != "blob" {
			return nil, invalid("missing or invalid history blob")
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || size > maxRecord {
			return nil, invalid("history blob exceeds record bound")
		}
		sizes = append(sizes, size)
		out = rest
	}
	if out != "" {
		return nil, invalid("trailing history blob headers")
	}
	return sizes, nil
}
