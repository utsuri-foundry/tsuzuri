package tsuzuri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cachedRepository(t *testing.T, f testRepository) (*repository, string) {
	t.Helper()
	wrapper, counter := historyGitCounter(t)
	r, err := clientAt(t, f.clone).session(context.Background(), OperationOptions{
		GitExecutable: wrapper, Environment: append(os.Environ(), "HISTORY_COUNTER="+counter),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, counter
}

func TestImmutableReadsPreserveOrderTypesAndCancellation(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	r, counter := cachedRepository(t, f)
	ctx := context.Background()
	a := checkedGit(t, f.clone, "rev-parse", "HEAD:"+historyTaskPath)
	b := checkedGit(t, f.clone, "rev-parse", "HEAD:.tsuzuri.toml")
	first, err := r.objects(ctx, []string{a, b, a}, "blob")
	if err != nil || len(first) != 3 || first[0] != testTask || first[1] != testConfig || first[2] != testTask {
		t.Fatalf("initial batch: %v", err)
	}
	calls := countHistoryGit(t, counter)
	second, err := r.objects(ctx, []string{b, a, b}, "blob")
	if err != nil || len(second) != 3 || second[0] != testConfig || second[1] != testTask || second[2] != testConfig || countHistoryGit(t, counter) != calls {
		t.Fatalf("cached order or unnecessary subprocess: %v", err)
	}
	if _, err = r.objects(ctx, []string{a}, "commit"); err == nil {
		t.Fatal("cached blob accepted as a commit")
	}
	for _, id := range []string{"HEAD", strings.Repeat("0", 40)} {
		if _, err = r.objects(ctx, []string{id}, "blob"); err == nil {
			t.Fatalf("invalid or missing object accepted: %s", id)
		}
	}
	later, err := r.blob(ctx, "available later", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.objects(ctx, []string{later}, "blob"); err == nil {
		t.Fatal("missing object unexpectedly readable")
	}
	if _, err = r.blob(ctx, "available later", true); err != nil {
		t.Fatal(err)
	}
	if _, err = r.objects(ctx, []string{later}, "blob"); err != nil {
		t.Fatalf("failed read was cached: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = r.objects(cancelled, []string{a}, "blob"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache bypassed cancellation: %v", err)
	}
	// Expansion of repeated cache hits must retain the normal subprocess output cap.
	large, err := r.blob(ctx, strings.Repeat("x", maxRecord), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.objects(ctx, []string{large}, "blob"); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, maxOutput/maxRecord+1)
	for n := range ids {
		ids[n] = large
	}
	if _, err = r.objects(ctx, ids, "blob"); err == nil || !strings.Contains(err.Error(), "exceeded bound") {
		t.Fatalf("cached aggregate output was unbounded: %v", err)
	}
}

func TestImmutableResolutionNeverCachesMutableRefs(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	r, counter := cachedRepository(t, f)
	ctx := context.Background()
	base, err := r.oid(ctx, "HEAD^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"^{commit}", "^{tree}"} {
		rev := base + suffix
		first, err := r.oid(ctx, rev)
		if err != nil {
			t.Fatal(err)
		}
		calls := countHistoryGit(t, counter)
		second, err := r.oid(ctx, rev)
		if err != nil || first != second || countHistoryGit(t, counter) != calls {
			t.Fatalf("immutable read not reused: %v", err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err = r.oid(cancelled, rev); !errors.Is(err, context.Canceled) {
			t.Fatalf("cached resolution bypassed cancellation: %v", err)
		}
	}
	historyCommit(t, f)
	tip, err := r.oid(ctx, "HEAD^{commit}")
	if err != nil || tip == base {
		t.Fatalf("mutable ref reused a stale resolution: %v", err)
	}
	other, _ := cachedRepository(t, f)
	if len(other.cache.objects)+len(other.cache.records)+len(other.cache.resolutions) != 0 {
		t.Fatal("operation inherited cached observations")
	}
}

func TestRecordReuseStillValidatesEachInventory(t *testing.T) {
	isolatedGit(t)
	for _, tc := range []struct {
		name, want string
		edit       func(*testing.T, testRepository)
	}{
		{"task path", "invalid task path", func(t *testing.T, f testRepository) {
			checkedGit(t, f.clone, "mv", historyTaskPath, "tasks/projects/example/tasks/wrong.task.md")
		}},
		{"project path", "invalid project path", func(t *testing.T, f testRepository) {
			checkedGit(t, f.clone, "mv", "tasks/projects/example", "tasks/projects/wrong")
		}},
		{"registries", "unregistered area", func(t *testing.T, f testRepository) {
			writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), strings.Replace(testConfig, `areas = ["core"]`, `areas = []`, 1))
		}},
		{"project state", "completed project", func(t *testing.T, f testRepository) {
			writeTestFile(t, filepath.Join(f.clone, "tasks/projects/example/project.md"), strings.Replace(testProject, `state = "active"`, `state = "completed"`, 1))
		}},
		{"missing project", "missing project", func(t *testing.T, f testRepository) {
			checkedGit(t, f.clone, "rm", "tasks/projects/example/project.md")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			r, _ := cachedRepository(t, f)
			if _, err := r.tree(context.Background(), "HEAD", false, true); err != nil {
				t.Fatal(err)
			}
			tc.edit(t, f)
			historyCommit(t, f)
			if _, err := r.tree(context.Background(), "HEAD", false, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("cached records bypassed revision validation: %v", err)
			}
		})
	}
}

func TestRecordReuseSeparatesParserModesAndPaths(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	historyWrite(t, f, historyTaskPath, strings.Replace(testTask, "kind = \"delivery\"\n", "", 1))
	r, _ := cachedRepository(t, f)
	if _, err := r.tree(context.Background(), "HEAD", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.tree(context.Background(), "HEAD", false, true); err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("legacy parse bypassed strict parser: %v", err)
	}
	loaded, err := r.tree(context.Background(), "HEAD", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.addRecord(newInventory(loaded.Config), "tasks/projects/first-task/project.md", loaded.Documents[historyTaskPath], loaded.Digests[historyTaskPath], true); err == nil {
		t.Fatal("task parse reused as a project")
	}
	// The same blobs may move with the configured root; the first inventory must
	// keep its own paths while the second validates against the new root.
	f = fixture(t)
	r, _ = cachedRepository(t, f)
	before, err := r.tree(context.Background(), "HEAD", false, true)
	if err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.clone, "mv", "tasks/projects", "backlog")
	writeTestFile(t, filepath.Join(f.clone, ".tsuzuri.toml"), strings.ReplaceAll(testConfig, "tasks/projects", "backlog"))
	historyCommit(t, f)
	after, err := r.tree(context.Background(), "HEAD", false, true)
	if err != nil || before.Tasks["first-task"].Path != historyTaskPath || after.Tasks["first-task"].Path != "backlog/example/tasks/first-task.task.md" {
		t.Fatalf("cached record path contaminated an inventory: %v", err)
	}
	if err = r.addRecord(after, "backlog/example/tasks/first-task.task.md", testTask, before.Digests[historyTaskPath], false); err == nil || !strings.Contains(err.Error(), "duplicate task") {
		t.Fatalf("cache bypassed duplicate detection: %v", err)
	}
}

func TestCachedTaskStillRequiresCurrentDependencies(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	dependencyPath := "tasks/projects/example/tasks/second-task.task.md"
	writeTestFile(t, filepath.Join(f.clone, dependencyPath), strings.ReplaceAll(testTask, "first-task", "second-task"))
	writeTestFile(t, filepath.Join(f.clone, historyTaskPath), strings.Replace(testTask, "blocked_by = []", "blocked_by = [\"second-task\"]", 1))
	historyCommit(t, f)
	r, _ := cachedRepository(t, f)
	if _, err := r.tree(context.Background(), "HEAD", false, true); err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.clone, "rm", dependencyPath)
	historyCommit(t, f)
	if _, err := r.tree(context.Background(), "HEAD", false, true); err == nil || !strings.Contains(err.Error(), "invalid dependency") {
		t.Fatalf("cached task bypassed current dependency check: %v", err)
	}
}

func TestManyClaimObservationsWithAndWithoutReuse(t *testing.T) {
	isolatedGit(t)
	f := manyClaimFixture(t, 12)
	ctx := context.Background()
	warm, warmCounter := cachedRepository(t, f)
	s, err := warm.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	warmStart := countHistoryGit(t, warmCounter)
	observed := warm.observe(ctx, s)
	warmCalls := countHistoryGit(t, warmCounter) - warmStart
	cold, coldCounter := cachedRepository(t, f)
	if _, err = cold.snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	coldStart := countHistoryGit(t, coldCounter)
	var reference []ClaimObservation
	for _, id := range sortedKeys(s.claims) {
		// Re-run every claim's checks without reuse across claims, using the same
		// immutable snapshot. No alternate validation implementation is involved.
		cold.cache = repositoryCache{}
		cold.trees = map[string]*inventory{}
		one := *s
		one.claims = map[string]observedRef{id: s.claims[id]}
		reference = append(reference, cold.observe(ctx, &one)...)
	}
	coldCalls := countHistoryGit(t, coldCounter) - coldStart
	a, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || warmCalls >= coldCalls {
		t.Fatalf("observation changed or reads not reduced: warm=%d cold=%d equal=%t", warmCalls, coldCalls, string(a) == string(b))
	}
	invalid, terminal, invalidAssignments := 0, 0, 0
	for _, c := range observed {
		if !c.Valid {
			invalid++
		}
		if c.Status == "stale-terminal" {
			terminal++
		}
		if c.Assignment != nil && !c.Assignment.Valid {
			invalidAssignments++
		}
	}
	if invalid != 1 || terminal != 11 || invalidAssignments != 1 {
		t.Fatalf("lost terminal/invalid observations: %d/%d/%d", invalid, terminal, invalidAssignments)
	}
	t.Logf("claim inspection subprocesses: reused=%d isolated=%d", warmCalls, coldCalls)
}

func manyClaimFixture(t *testing.T, count int) testRepository {
	t.Helper()
	f := historyFixture(t, count, "add")
	r, err := clientAt(t, f.clone).session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	revisions := strings.Fields(checkedGit(t, f.clone, "rev-list", "--first-parent", "--reverse", "main"))
	for n, rev := range revisions {
		id := fmt.Sprintf("task-%06d", n)
		if n == 0 {
			id = "first-task"
		}
		loaded, err := r.tree(ctx, rev, false, true)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := r.newClaim(ctx, &snapshot{base: rev, loaded: loaded}, id, "test-owner")
		if err != nil {
			t.Fatal(err)
		}
		checkedGit(t, f.clone, "update-ref", loaded.Config.claimRef(id), claim.AnchorObjectID)
		if n == 0 {
			checkedGit(t, f.clone, "update-ref", loaded.Config.claimRef(id), rev)
		}
		if n == 1 {
			checkedGit(t, f.clone, "update-ref", loaded.Config.assignmentRef(id), rev)
		}
		body := strings.ReplaceAll(historyTerminal(), "first-task", id)
		writeTestFile(t, filepath.Join(f.clone, "tasks/projects/example/tasks/"+id+".task.md"), body)
	}
	historyCommit(t, f)
	checkedGit(t, f.clone, "push", "origin", "main", "refs/heads/tsuzuri/*:refs/heads/tsuzuri/*")
	return f
}

func TestReadCachesFallBackAtCapacity(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	r, counter := cachedRepository(t, f)
	ctx := context.Background()
	var ids []string
	for n := 0; n < maxCacheBytes/maxRecord+1; n++ {
		body := testTask + fmt.Sprintf("\nRecord %d\n", n) + strings.Repeat("x", maxRecord-len(testTask)-32)
		id, err := r.blob(ctx, body, true)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	texts, err := r.objects(ctx, ids, "blob")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(testConfig)
	if err != nil {
		t.Fatal(err)
	}
	for n, text := range texts {
		if err = r.addRecord(newInventory(cfg), historyTaskPath, text, ids[n], false); err != nil {
			t.Fatal(err)
		}
	}
	if r.cache.objectBytes > maxCacheBytes || r.cache.recordBytes > maxCacheBytes || len(r.cache.objects) >= len(ids) || len(r.cache.records) >= len(ids) {
		t.Fatal("cache retained inputs beyond its byte budget")
	}
	calls := countHistoryGit(t, counter)
	repeated, err := r.objects(ctx, ids, "blob")
	if err != nil || len(repeated) != len(texts) || countHistoryGit(t, counter) != calls+1 {
		t.Fatalf("capacity must fall back to a normal batch read: %v", err)
	}
	for n, text := range repeated {
		if text != texts[n] {
			t.Fatal("fallback changed blob content")
		}
		if err = r.addRecord(newInventory(cfg), historyTaskPath, text, ids[n], false); err != nil {
			t.Fatal(err)
		}
	}
	// Model a cache full of tiny entries: the byte budget is not the only bound.
	r.cache = repositoryCache{objects: map[objectKey]string{}, records: map[recordKey]parsedRecord{}, resolutions: map[string]string{}}
	for n := 0; n < maxCacheEntries; n++ {
		id := fmt.Sprintf("%040x", n)
		r.cache.objects[objectKey{id, "blob"}] = ""
		r.cache.records[recordKey{id: id}] = parsedRecord{}
		r.cache.resolutions[id+"^{tree}"] = id
	}
	base := checkedGit(t, f.clone, "rev-parse", "HEAD")
	calls = countHistoryGit(t, counter)
	for n := 0; n < 2; n++ {
		got, err := r.objects(ctx, ids[:1], "blob")
		if err != nil || len(got) != 1 || got[0] != texts[0] {
			t.Fatalf("entry capacity prevented a normal read: %v", err)
		}
		if err = r.addRecord(newInventory(cfg), historyTaskPath, got[0], ids[0], false); err != nil {
			t.Fatal(err)
		}
		if _, err = r.oid(ctx, base+"^{tree}"); err != nil {
			t.Fatal(err)
		}
	}
	if countHistoryGit(t, counter) != calls+4 || len(r.cache.objects) != maxCacheEntries || len(r.cache.records) != maxCacheEntries || len(r.cache.resolutions) != maxCacheEntries {
		t.Fatal("entry-capacity fallback did not preserve the bounds")
	}
}
