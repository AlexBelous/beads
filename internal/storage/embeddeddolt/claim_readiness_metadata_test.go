//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	domaindb "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Prior art: Stephanie Jarmak's upstream PR7049 pins dependency refusal at
// every claim door. This pinned compatibility profile is opt-in and retains
// the legacy default; it also pins the holder retry regression noted there.
func TestEmbeddedClaimReadinessMetadata(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, door := range []string{"store", "role", "repository"} {
		t.Run(door, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				metadata json.RawMessage
				blocked  bool
				refuse   bool
			}{
				{"enrolled blocked", json.RawMessage(`{"claim.require_unblocked":true}`), true, true},
				{"enrolled ready", json.RawMessage(`{"claim.require_unblocked":true}`), false, false},
				{"legacy unchanged", nil, true, false},
				{"explicit disabled", json.RawMessage(`{"claim.require_unblocked":false}`), true, false},
				{"unrelated metadata", json.RawMessage(`{"other":"value"}`), true, false},
				{"unknown constraint type", json.RawMessage(`{"claim.require_unblocked":"true"}`), false, true},
				{"unknown constraint null", json.RawMessage(`{"claim.require_unblocked":null}`), false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					te := newTestEnv(t, "rdy")
					ctx := t.Context()
					issue := &types.Issue{ID: "rdy-work", Title: "isolated claim", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: tc.metadata}
					if err := te.store.CreateIssue(ctx, issue, "fixture"); err != nil {
						t.Fatal(err)
					}
					if tc.blocked {
						blocker := &types.Issue{ID: "rdy-blocker", Title: "existing blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
						if err := te.store.CreateIssue(ctx, blocker, "fixture"); err != nil {
							t.Fatal(err)
						}
						if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: issue.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "fixture"); err != nil {
							t.Fatal(err)
						}
					}
					before, err := te.store.GetIssue(ctx, issue.ID)
					if err != nil {
						t.Fatal(err)
					}
					if door == "store" {
						err = te.store.ClaimIssue(ctx, issue.ID, "worker")
					} else if door == "repository" {
						err = func() error {
							db, cleanup, e := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
							if e != nil {
								return e
							}
							defer cleanup()
							tx, e := db.BeginTx(ctx, nil)
							if e != nil {
								return e
							}
							defer tx.Rollback()
							if _, e = domaindb.NewIssueSQLRepository(tx).Claim(ctx, issue.ID, "worker", domain.IssueTableOpts{}); e != nil {
								return e
							}
							return tx.Commit()
						}()
					} else {
						claimer, e := te.store.IssueClaimer()
						if e != nil {
							t.Fatal(e)
						}
						_, err = claimer.Claim(ctx, publicops.ClaimRequest{IssueID: issue.ID, Actor: "worker"})
					}
					after, e := te.store.GetIssue(ctx, issue.ID)
					if e != nil {
						t.Fatal(e)
					}
					if tc.refuse {
						if err == nil {
							t.Fatalf("claim admitted despite %s", tc.name)
						}
						if tc.blocked && !errors.Is(err, storage.ErrNotClaimable) {
							t.Errorf("refusal=%v; want not claimable", err)
						}
						if after.Status != before.Status || after.Assignee != before.Assignee || after.RowVersion != before.RowVersion || !after.UpdatedAt.Equal(before.UpdatedAt) || after.StartedAt != nil {
							t.Fatalf("refused claim changed row: before=%+v after=%+v", before, after)
						}
					} else if err != nil || after.Status != types.StatusInProgress || after.Assignee != "worker" {
						t.Fatalf("positive control failed: err=%v status=%s assignee=%q", err, after.Status, after.Assignee)
					}
				})
			}
		})
	}
}

func TestEmbeddedClaimReadinessHolderRetry(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rhr")
	ctx := t.Context()
	issue := &types.Issue{ID: "rhr-work", Title: "holder retry", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"claim.require_unblocked":true}`)}
	blocker := &types.Issue{ID: "rhr-blocker", Title: "new prerequisite", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, row := range []*types.Issue{issue, blocker} {
		if err := te.store.CreateIssue(ctx, row, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	claimer, err := te.store.IssueClaimer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = claimer.Claim(ctx, publicops.ClaimRequest{IssueID: issue.ID, Actor: "worker"}); err != nil {
		t.Fatal(err)
	}
	if err = te.store.AddDependency(ctx, &types.Dependency{IssueID: issue.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "fixture"); err != nil {
		t.Fatal(err)
	}
	before, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = te.store.ClaimIssue(ctx, issue.ID, "worker"); err != nil {
		t.Fatalf("store holder retry=%v", err)
	}
	result, err := claimer.Claim(ctx, publicops.ClaimRequest{IssueID: issue.ID, Actor: "worker"})
	if err != nil || result.Changed {
		t.Fatalf("role holder retry: result=%+v err=%v", result, err)
	}
	after, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RowVersion != before.RowVersion || after.Assignee != before.Assignee || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("idempotent holder retry changed the row")
	}
}

func TestEmbeddedClaimReadinessRemainingDependency(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rrd")
	ctx := t.Context()
	for _, id := range []string{"rrd-work", "rrd-first", "rrd-second"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if id == "rrd-work" {
			issue.Metadata = json.RawMessage(`{"claim.require_unblocked":true}`)
		}
		if err := te.store.CreateIssue(ctx, issue, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"rrd-first", "rrd-second"} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "rrd-work", DependsOnID: id, Type: types.DepBlocks}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if err := te.store.CloseIssue(ctx, "rrd-first", "first prerequisite done", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := te.store.ClaimIssue(ctx, "rrd-work", "worker"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("remaining blocker was bypassed: %v", err)
	}
	after, err := te.store.GetIssue(ctx, "rrd-work")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != types.StatusOpen || after.Assignee != "" {
		t.Fatalf("refusal changed ownership: %+v", after)
	}
	te.assertEventCount(t, ctx, "events", "rrd-work", "claimed", 0)
	if err := te.store.CloseIssue(ctx, "rrd-second", "last prerequisite done", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := te.store.ClaimIssue(ctx, "rrd-work", "worker"); err != nil {
		t.Fatalf("all prerequisites cleared but claim refused: %v", err)
	}
	after, err = te.store.GetIssue(ctx, "rrd-work")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != types.StatusInProgress || after.Assignee != "worker" {
		t.Fatalf("claim did not land: %+v", after)
	}
	te.assertEventCount(t, ctx, "events", "rrd-work", "claimed", 1)
}
