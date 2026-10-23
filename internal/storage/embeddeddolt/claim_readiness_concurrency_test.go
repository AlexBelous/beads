//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	domaindb "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// A blocker committed by a concurrent transaction must invalidate the staged
// opted-in claim. Two sessions share a real embedded connector; no sleeps.
func TestEmbeddedClaimReadinessConcurrentDependencyBoundary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, wisp := range []bool{false, true} {
		for _, door := range []string{"common", "repository"} {
			for _, writer := range []string{"add", "prechecked add", "repository add", "reopen", "inherited", "repair"} {
				t.Run(fmt.Sprintf("wisp=%t/%s/%s", wisp, door, writer), func(t *testing.T) {
					te := newTestEnv(t, "rcb")
					ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
					defer cancel()
					table, deps, events := "issues", "dependencies", "events"
					if wisp {
						table, deps, events = "wisps", "wisp_dependencies", "wisp_events"
					}
					for _, id := range []string{"rcb-work", "rcb-blocker", "rcb-parent"} {
						row := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
						if id == "rcb-work" {
							row.Ephemeral = wisp
							row.Metadata = json.RawMessage(`{"claim.require_unblocked":true}`)
						}
						if err := te.store.CreateIssue(ctx, row, "fixture"); err != nil {
							t.Fatal(err)
						}
					}
					if writer == "reopen" {
						if err := te.store.CloseIssue(ctx, "rcb-blocker", "initially done", "fixture", ""); err != nil {
							t.Fatal(err)
						}
					}
					if writer == "reopen" || writer == "repair" || writer == "inherited" {
						target, kind := "rcb-blocker", types.DepBlocks
						if writer == "inherited" {
							target, kind = "rcb-parent", types.DepParentChild
						}
						if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "rcb-work", DependsOnID: target, Type: kind}, "fixture"); err != nil {
							t.Fatal(err)
						}
					}
					if writer == "repair" {
						// An isolated corrupt aux flag is the full-repair input; the
						// dependency remains real and no production data is involved.
						te.exec(t, ctx, "UPDATE "+table+" SET is_blocked = 0 WHERE id = ?", "rcb-work")
					}
					err := func() error {
						db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
						if err != nil {
							return err
						}
						defer cleanup()
						claimTx, err := db.BeginTx(ctx, nil)
						if err != nil {
							return err
						}
						defer claimTx.Rollback()
						if door == "common" {
							_, err = issueops.ClaimIssueInTx(ctx, claimTx, "rcb-work", "worker")
						} else {
							_, err = domaindb.NewIssueSQLRepository(claimTx).Claim(ctx, "rcb-work", "worker", domain.IssueTableOpts{UseWispsTable: wisp})
						}
						if err != nil {
							return err
						}
						depTx, err := db.BeginTx(ctx, nil)
						if err != nil {
							return err
						}
						defer depTx.Rollback()
						if db.Stats().InUse != 2 {
							return fmt.Errorf("want two concurrent SQL sessions")
						}
						if err := addClaimRaceBlocker(ctx, depTx, writer, wisp); err != nil {
							return err
						}
						if err := depTx.Commit(); err != nil {
							return err
						}
						var status string
						var blocked bool
						if err := db.QueryRowContext(ctx, "SELECT status, is_blocked FROM "+table+" WHERE id = ?", "rcb-work").Scan(&status, &blocked); err != nil {
							return err
						}
						if status != "open" || !blocked {
							return fmt.Errorf("committed blocker state: %s/%t", status, blocked)
						}
						err = claimTx.Commit()
						t.Logf("claim commit after %s: %v", writer, err)
						var conflict *mysql.MySQLError
						if !errors.As(err, &conflict) || conflict.Number != 1213 {
							return fmt.Errorf("want serialization conflict 1213, got %v", err)
						}
						return nil
					}()
					if err != nil {
						t.Fatalf("concurrent schedule: %v", err)
					}
					after, err := te.store.GetIssue(ctx, "rcb-work")
					if err != nil {
						t.Fatal(err)
					}
					if after.Status != types.StatusOpen || after.Assignee != "" || after.StartedAt != nil {
						t.Fatalf("failed claim changed ownership: %+v", after)
					}
					var blocked bool
					var edges, leases int
					te.queryScalar(t, ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", []any{"rcb-work"}, &blocked)
					te.queryScalar(t, ctx, "SELECT COUNT(*) FROM "+deps+" WHERE issue_id = ?", []any{"rcb-work"}, &edges)
					te.queryScalar(t, ctx, "SELECT COUNT(*) FROM leases WHERE issue_id = ?", []any{"rcb-work"}, &leases)
					if !blocked || edges != 1 || leases != 0 {
						t.Fatalf("persisted blocker/edges/leases=%t/%d/%d", blocked, edges, leases)
					}
					te.assertEventCount(t, ctx, events, "rcb-work", "claimed", 0)
					if err := te.store.ClaimIssue(ctx, "rcb-work", "worker"); !errors.Is(err, storage.ErrNotClaimable) {
						t.Fatalf("fresh retry: %v", err)
					}
					if err := te.store.CloseIssue(ctx, "rcb-blocker", "prerequisite done", "fixture", ""); err != nil {
						t.Fatal(err)
					}
					if err := te.store.ClaimIssue(ctx, "rcb-work", "worker"); err != nil {
						t.Fatalf("claim after prerequisite closes: %v", err)
					}
					te.assertEventCount(t, ctx, events, "rcb-work", "claimed", 1)
				})
			}
		}
	}
}

func addClaimRaceBlocker(ctx context.Context, tx *sql.Tx, writer string, wisp bool) error {
	dep := &types.Dependency{IssueID: "rcb-work", DependsOnID: "rcb-blocker", Type: types.DepBlocks}
	switch writer {
	case "repository add":
		return domaindb.NewDependencySQLRepository(tx).Insert(ctx, dep, "fixture", domain.DepInsertOpts{UseWispsTable: wisp, EmitEvent: true})
	case "reopen":
		_, err := issueops.ReopenIssueInTx(ctx, tx, "rcb-blocker", "", "fixture")
		return err
	case "repair":
		_, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
		return err
	case "inherited":
		dep.IssueID = "rcb-parent"
	}
	opts := issueops.AddDependencyOpts{EmitEvent: true}
	if writer == "prechecked add" {
		kind := issueops.DepTargetIssue
		opts.TargetKind = &kind
		opts.PrecheckedTarget = &issueops.DepTargetPrecheck{IssueType: string(types.TypeTask), Status: string(types.StatusOpen)}
	}
	_, err := issueops.AddDependencyInTx(ctx, tx, dep, "fixture", opts)
	return err
}

func TestEmbeddedClaimReadinessAuxVersionCompatibility(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, wisp := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			metadata json.RawMessage
			advances bool
		}{
			{"opted in", json.RawMessage(`{"claim.require_unblocked":true}`), true},
			{"legacy", nil, false},
			{"disabled", json.RawMessage(`{"claim.require_unblocked":false}`), false},
			{"string", json.RawMessage(`{"claim.require_unblocked":"true"}`), false},
			{"number", json.RawMessage(`{"claim.require_unblocked":1}`), false},
			{"null", json.RawMessage(`{"claim.require_unblocked":null}`), false},
		} {
			t.Run(fmt.Sprintf("wisp=%t/%s", wisp, tc.name), func(t *testing.T) {
				te := newTestEnv(t, "rvc")
				ctx := t.Context()
				for _, row := range []*types.Issue{
					{ID: "rvc-work", Title: "work", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp, Metadata: tc.metadata},
					{ID: "rvc-blocker", Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				} {
					if err := te.store.CreateIssue(ctx, row, "fixture"); err != nil {
						t.Fatal(err)
					}
				}
				before, err := te.store.GetIssue(ctx, "rvc-work")
				if err != nil {
					t.Fatal(err)
				}
				dep := &types.Dependency{IssueID: "rvc-work", DependsOnID: "rvc-blocker", Type: types.DepBlocks}
				if err := te.store.AddDependency(ctx, dep, "fixture"); err != nil {
					t.Fatal(err)
				}
				marked, err := te.store.GetIssue(ctx, "rvc-work")
				if err != nil {
					t.Fatal(err)
				}
				if (marked.RowVersion != before.RowVersion) != tc.advances || !marked.UpdatedAt.Equal(before.UpdatedAt) {
					t.Fatal("blocking mark changed wrong version/timestamp")
				}
				if err := te.store.AddDependency(ctx, dep, "fixture"); err != nil {
					t.Fatal(err)
				}

				if err := func() error {
					db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
					if err != nil {
						return err
					}
					defer cleanup()
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						return err
					}
					defer tx.Rollback()
					changed, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
					if err != nil {
						return err
					}
					if changed != 0 {
						return fmt.Errorf("no-op repair changed %d rows", changed)
					}
					return tx.Commit()
				}(); err != nil {
					t.Fatal(err)
				}
				again, err := te.store.GetIssue(ctx, "rvc-work")
				if err != nil {
					t.Fatal(err)
				}
				if again.RowVersion != marked.RowVersion || !again.UpdatedAt.Equal(marked.UpdatedAt) {
					t.Fatal("no-op mark changed version/timestamp")
				}
				if err := te.store.CloseIssue(ctx, "rvc-blocker", "done", "fixture", ""); err != nil {
					t.Fatal(err)
				}
				unmarked, err := te.store.GetIssue(ctx, "rvc-work")
				if err != nil {
					t.Fatal(err)
				}
				if unmarked.RowVersion != marked.RowVersion || !unmarked.UpdatedAt.Equal(marked.UpdatedAt) {
					t.Fatal("unmark changed version/timestamp")
				}
				err = te.store.UpdateIssueChecked(ctx, "rvc-work", map[string]interface{}{"title": "updated"}, "fixture", storage.UpdateIssueOptions{ExpectedVersion: &before.RowVersion})
				if tc.advances {
					if !errors.Is(err, storage.ErrVersionMismatch) {
						t.Fatalf("stale opted-in ExpectedVersion: %v", err)
					}
					err = te.store.UpdateIssueChecked(ctx, "rvc-work", map[string]interface{}{"title": "updated"}, "fixture", storage.UpdateIssueOptions{ExpectedVersion: &unmarked.RowVersion})
				}
				if err != nil {
					t.Fatalf("honest ExpectedVersion update refused: %v", err)
				}
			})
		}
	}
}
