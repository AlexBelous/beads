package issueops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ClaimRequireUnblockedMetadataKey is an opt-in, stored dependency constraint.
// It is not a permission, caller identity or an external-registry attestation.
const ClaimRequireUnblockedMetadataKey = "claim.require_unblocked"

// EnforceClaimUnblockedInTx checks an opted-in issue before a claim writes any
// ownership, lease or event. The caller must supply the canonical pre-image
// read within tx, and use that same transaction for the claim mutation.
// An existing holder's idempotent in_progress retry retains its old contract.
// Legacy issues without the constraint keep their existing claim behavior.
func EnforceClaimUnblockedInTx(ctx context.Context, tx DBTX, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("claim dependency constraint: canonical issue unavailable")
	}
	// A holder retry admits no new work and writes nothing, even when the
	// stored constraint or dependencies have changed since the first claim.
	if issue.Status == types.StatusInProgress && ActorMatches(issue.Assignee, actor) {
		return nil
	}
	raw := bytes.TrimSpace(issue.Metadata)
	// Valid non-object metadata has no top-level constraint. Do not reinterpret
	// ordinary legacy metadata or a quoted string as an enrollment.
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("claim dependency constraint: invalid metadata: %w", err)
	}
	value, present := fields[ClaimRequireUnblockedMetadataKey]
	if !present {
		return nil
	}
	var required bool
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("claim dependency constraint must be boolean")
	}
	if err := json.Unmarshal(value, &required); err != nil {
		return fmt.Errorf("claim dependency constraint must be boolean: %w", err)
	}
	if !required {
		return nil
	}
	blocked, blockers, err := IsBlockedInTx(ctx, tx, issue.ID)
	if err != nil {
		return fmt.Errorf("claim dependency constraint unavailable: %w", err)
	}
	if blocked {
		if len(blockers) == 0 {
			return fmt.Errorf("%w: %s has an inherited or unresolved block", storage.ErrNotClaimable, issue.ID)
		}
		return fmt.Errorf("%w: %s is blocked by %s", storage.ErrNotClaimable, issue.ID, strings.Join(blockers, ", "))
	}
	return nil
}

// BlockingReadinessRowLockClause joins the existing claim conflict token only
// for stored boolean opt-ins. Use it in an UPDATE already restricted to an
// actual is_blocked 0->1 transition. For these issues a newly blocking dependency
// changes admission, so RowVersion intentionally advances. Legacy auxiliary
// refreshes keep their token and timestamp. alias must be a fixed table alias.
func BlockingReadinessRowLockClause(alias string) string {
	return fmt.Sprintf(`%[1]s.row_lock = CASE
        WHEN JSON_TYPE(JSON_EXTRACT(%[1]s.metadata, '$."claim.require_unblocked"')) = 'BOOLEAN'
         AND JSON_UNQUOTE(JSON_EXTRACT(%[1]s.metadata, '$."claim.require_unblocked"')) = 'true'
        THEN %[2]d ELSE %[1]s.row_lock END`, alias, freshRowLock())
}
