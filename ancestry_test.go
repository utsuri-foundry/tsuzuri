package tsuzuri

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func ancestryCalls(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), " merge-base --is-ancestor ")
}

func TestDriftReusesOnlyProvenFirstParentAncestry(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	base := checkedGit(t, f.clone, "rev-parse", "HEAD")
	checkedGit(t, f.clone, "checkout", "-b", "side")
	checkedGit(t, f.clone, "commit", "--allow-empty", "-m", "side branch")
	side := checkedGit(t, f.clone, "rev-parse", "HEAD")
	checkedGit(t, f.clone, "checkout", "main")
	checkedGit(t, f.clone, "commit", "--allow-empty", "-m", "main branch")
	main := checkedGit(t, f.clone, "rev-parse", "HEAD")
	r, counter := cachedRepository(t, f)
	ctx := context.Background()
	// Validate the side branch first; that proof must not confer ancestry to main.
	if _, err := r.tree(ctx, side, true, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.tree(ctx, main, false, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &snapshot{base: main, loaded: loaded}
	for _, tc := range []struct {
		name, from, want string
		calls            int
	}{
		{"first parent", base, "compatible-base-advancement", 0},
		{"validated side branch", side, "history-rewritten", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := ancestryCalls(t, counter)
			drift := r.drift(ctx, s, ClaimMetadata{TaskID: "first-task", BaseObjectID: tc.from})
			if len(drift) != 1 || drift[0] != tc.want || ancestryCalls(t, counter)-before != tc.calls {
				t.Fatalf("drift=%v; ancestry calls=%d", drift, ancestryCalls(t, counter)-before)
			}
		})
	}
	checkedGit(t, f.clone, "merge", "--no-ff", "side", "-m", "merge side branch")
	merged := checkedGit(t, f.clone, "rev-parse", "HEAD")
	loaded, err = r.tree(ctx, merged, false, true)
	if err != nil {
		t.Fatal(err)
	}
	before := ancestryCalls(t, counter)
	drift := r.drift(ctx, &snapshot{base: merged, loaded: loaded}, ClaimMetadata{TaskID: "first-task", BaseObjectID: side})
	if len(drift) != 1 || drift[0] != "compatible-base-advancement" || ancestryCalls(t, counter) != before+1 {
		t.Fatalf("second-parent ancestry must use the full Git graph: %v", drift)
	}
}

func TestAncestryProofCapacityFallsBackToGit(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	base := checkedGit(t, f.clone, "rev-parse", "HEAD")
	historyCommit(t, f)
	tip := checkedGit(t, f.clone, "rev-parse", "HEAD")
	r, counter := cachedRepository(t, f)
	r.cache.firstParents = map[ancestryKey]struct{}{}
	for n := 0; n < maxCacheEntries; n++ {
		r.cache.firstParents[ancestryKey{fmt.Sprintf("%040x", n), tip}] = struct{}{}
	}
	loaded, err := r.tree(context.Background(), tip, false, true)
	if err != nil {
		t.Fatal(err)
	}
	before := ancestryCalls(t, counter)
	drift := r.drift(context.Background(), &snapshot{base: tip, loaded: loaded}, ClaimMetadata{TaskID: "first-task", BaseObjectID: base})
	if len(drift) != 1 || drift[0] != "compatible-base-advancement" || ancestryCalls(t, counter) != before+1 || len(r.cache.firstParents) != maxCacheEntries {
		t.Fatalf("bounded proof cache changed ancestry checks: %v", drift)
	}
}

func TestFailedHistoryDoesNotSupplyAncestryProof(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	historyRemove(t, f, historyTaskPath)
	historyWrite(t, f, historyTaskPath, testTask)
	r, _ := cachedRepository(t, f)
	if _, err := r.tree(context.Background(), "HEAD", false, true); err == nil || !strings.Contains(err.Error(), "removed and reused") {
		t.Fatalf("invalid history accepted: %v", err)
	}
	if len(r.cache.firstParents) != 0 {
		t.Fatal("failed history scan supplied ancestry evidence")
	}
}

func TestTreeConfigurationReusesBlobReads(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	r, counter := cachedRepository(t, f)
	ctx := context.Background()
	first, err := r.tree(ctx, "HEAD", false, true)
	if err != nil {
		t.Fatal(err)
	}
	historyCommit(t, f)
	second, err := r.tree(ctx, "HEAD", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if *first.Source.ConfigurationObjectID != *second.Source.ConfigurationObjectID || first.Config.RepositoryID != second.Config.RepositoryID {
		t.Fatal("cached configuration changed its identity")
	}
	commands, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), " show ") {
		t.Fatal("configuration bypassed the immutable blob reader")
	}
}

func TestClaimAnchorCountIncludesFoldedSubjects(t *testing.T) {
	isolatedGit(t)
	f := fixture(t)
	client := clientAt(t, f.clone)
	claim := mustClaim(t, client, "worker-a")
	r, err := client.session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Git's %s folds this malformed subject into the valid anchor's subject.
	// A literal full-subject --grep would hide it and miss the duplicate anchor.
	tip, err := r.commitMetadata(context.Background(), claim.Metadata.AnchorObjectID, "tsuzuri claim\nfirst-task\n\nMalformed duplicate\n", claim.Metadata.ClaimedAt)
	if err != nil {
		t.Fatal(err)
	}
	checkedGit(t, f.clone, "push", "origin", tip+":"+claim.RemoteRef)
	view, _, err := client.Show(context.Background(), "first-task")
	if err != nil || view.Claim == nil || view.Claim.Valid || view.Claim.Error == nil || !strings.Contains(*view.Claim.Error, "found 2") {
		t.Fatalf("duplicate normalized subject was ignored: claim=%+v err=%v", view.Claim, err)
	}
}
