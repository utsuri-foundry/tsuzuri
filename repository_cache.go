package tsuzuri

import (
	"context"
	"strconv"
	"strings"
)

const maxCacheEntries = 4096
const maxCacheBytes = 8 << 20

type objectKey struct{ id, kind string }
type recordKey struct {
	id              string
	project, legacy bool
}
type parsedRecord struct {
	project Project
	task    Task
}

// Owned by one operation. Each content cache admits at most 8 MiB of input and
// 4096 entries; parsed Go values add bounded overhead, not an exact heap limit.
// Cached records are read-only; revision-specific validation is never cached here.
type repositoryCache struct {
	objects                  map[objectKey]string
	records                  map[recordKey]parsedRecord
	resolutions              map[string]string
	objectBytes, recordBytes int
}

func immutableRevision(rev string) bool {
	for _, suffix := range []string{"^{commit}", "^{tree}"} {
		if strings.HasSuffix(rev, suffix) && validObject(strings.TrimSuffix(rev, suffix)) {
			return true
		}
	}
	return false
}

func (r *repository) objects(ctx context.Context, ids []string, kind string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, invalid("Git operation cancelled or timed out: %v", err).causedBy(err)
	}
	values := make([]string, 0, len(ids))
	missing := []string{}
	resolved := map[string]string{}
	for _, id := range ids {
		if !validObject(id) {
			return nil, invalid("invalid object ID")
		}
		if _, ok := resolved[id]; ok {
			continue
		}
		value, ok := r.cache.objects[objectKey{id, kind}]
		resolved[id] = value
		if !ok {
			missing = append(missing, id)
		}
	}
	texts, err := r.readObjects(ctx, missing, kind)
	if err != nil {
		return nil, err
	}
	for n, id := range missing {
		resolved[id] = texts[n]
	}
	// Cache hits and duplicate IDs must still obey the original batch output bound.
	size := 0
	for _, id := range ids {
		value := resolved[id]
		size += len(id) + len(kind) + len(strconv.Itoa(len(value))) + len(value) + 4
		if size > maxOutput {
			return nil, invalid("Git output exceeded bound")
		}
	}
	for n, id := range missing {
		value := texts[n]
		if len(r.cache.objects) < maxCacheEntries && len(value) <= maxCacheBytes-r.cache.objectBytes {
			if r.cache.objects == nil {
				r.cache.objects = map[objectKey]string{}
			}
			value = strings.Clone(value)
			r.cache.objects[objectKey{strings.Clone(id), strings.Clone(kind)}] = value
			r.cache.objectBytes += len(value)
			resolved[id] = value
		}
	}
	for _, id := range ids {
		values = append(values, resolved[id])
	}
	return values, nil
}

func (r *repository) addRecord(i *inventory, path, text, id string, legacy bool) error {
	key := recordKey{id: id, project: strings.HasSuffix(path, "/project.md"), legacy: legacy}
	record, ok := r.cache.records[key]
	if !ok {
		// Do not retain the complete cat-file batch through parser string slices.
		text = strings.Clone(text)
		var err error
		if key.project {
			record.project, err = parseProject(text, "")
		} else {
			record.task, err = parseTask(text, "", legacy)
		}
		if err != nil {
			return err
		}
		if len(r.cache.records) < maxCacheEntries && len(text) <= maxCacheBytes-r.cache.recordBytes {
			if r.cache.records == nil {
				r.cache.records = map[recordKey]parsedRecord{}
			}
			key.id = strings.Clone(key.id)
			r.cache.records[key] = record
			r.cache.recordBytes += len(text)
		}
	}
	if key.project {
		return i.addProject(record.project, path, text, id)
	}
	return i.addTask(record.task, path, text, id)
}
