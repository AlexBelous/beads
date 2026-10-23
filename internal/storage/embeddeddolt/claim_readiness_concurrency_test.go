//go:build cgo

package embeddeddolt_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	domaindb "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// This is a characterization of the pinned prototype's concurrency boundary,
// not an admission test for a complete guard. These operations overlap: this
// does not establish a violation of ordinary transaction linearizability.
// It demonstrates that the dependency predicate need not hold at claim commit. Separate CLI/store connectors may
// serialize at open; here two real SQL sessions share one embedded connector.
func TestEmbeddedClaimReadinessConcurrentDependencyBoundary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, door := range []string{"common", "repository"} {
		t.Run(door, func(t *testing.T) {
			te := newTestEnv(t, "rcb")
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			for _, id := range []string{"rcb-work", "rcb-blocker"} {
				row := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if id == "rcb-work" {
					row.Metadata = json.RawMessage(`{"claim.require_unblocked":true}`)
				}
				if err := te.store.CreateIssue(ctx, row, "fixture"); err != nil {
					t.Fatal(err)
				}
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
				var originalVersion int64
				if err := claimTx.QueryRowContext(ctx, "SELECT row_lock FROM issues WHERE id = ?", "rcb-work").Scan(&originalVersion); err != nil {
					return err
				}
				// Stage the real claim (including its readiness read), but do
				// not commit it until the dependency transaction commits.
				if door == "common" {
					_, err = issueops.ClaimIssueInTx(ctx, claimTx, "rcb-work", "worker")
				} else {
					_, err = domaindb.NewIssueSQLRepository(claimTx).Claim(ctx, "rcb-work", "worker", domain.IssueTableOpts{})
				}
				if err != nil {
					return err
				}
				depTx, err := db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer depTx.Rollback()
				t.Logf("two transactions active: connections in use=%d", db.Stats().InUse)
				if _, err := issueops.AddDependencyInTx(ctx, depTx, &types.Dependency{IssueID: "rcb-work", DependsOnID: "rcb-blocker", Type: types.DepBlocks}, "fixture", issueops.AddDependencyOpts{EmitEvent: true}); err != nil {
					return err
				}
				if err := depTx.Commit(); err != nil {
					return err
				}
				var status string
				var blocked bool
				var dependencyVersion int64
				if err := db.QueryRowContext(ctx, "SELECT status, is_blocked, row_lock FROM issues WHERE id = ?", "rcb-work").Scan(&status, &blocked, &dependencyVersion); err != nil {
					return err
				}
				t.Logf("dependency committed before claim: status=%s is_blocked=%t ownership_version_unchanged=%t", status, blocked, dependencyVersion == originalVersion)
				if status != "open" || !blocked || dependencyVersion != originalVersion {
					return fmt.Errorf("dependency pre-claim state: status=%s blocked=%t version_unchanged=%t", status, blocked, dependencyVersion == originalVersion)
				}
				var snapshotBlocked bool
				if err := claimTx.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", "rcb-work").Scan(&snapshotBlocked); err != nil {
					return err
				}
				t.Logf("claim transaction still sees is_blocked=%t", snapshotBlocked)
				if snapshotBlocked {
					return fmt.Errorf("claim snapshot unexpectedly refreshed")
				}
				err = claimTx.Commit()
				t.Logf("claim commit after dependency: %v", err)
				return err
			}()
			if err != nil {
				t.Fatalf("concurrent schedule: %v", err)
			}
			after, err := te.store.GetIssue(ctx, "rcb-work")
			if err != nil {
				t.Fatal(err)
			}
			var blocked bool
			var edges int
			te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{"rcb-work"}, &blocked)
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{"rcb-work"}, &edges)
			t.Logf("fresh reopened result: status=%s assignee=%q is_blocked=%t dependency_count=%d", after.Status, after.Assignee, blocked, edges)
			if after.Status != types.StatusInProgress || after.Assignee != "worker" || !blocked || edges != 1 {
				t.Fatalf("pinned boundary changed: row=%+v blocked=%t edges=%d", after, blocked, edges)
			}
			te.assertEventCount(t, ctx, "events", "rcb-work", "claimed", 1)
			te.assertEventCount(t, ctx, "events", "rcb-work", "dependency_added", 1)
		})
	}
}
