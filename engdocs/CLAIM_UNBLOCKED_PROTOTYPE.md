# Opt-in native claim dependency constraint

Journal: https://github.com/AlexBelous/gascity-kb/issues/128

On pinned Beads da08f2739, exact-ID claim accepts an open issue even when a
blocking dependency remains. Existing claim behavior remains unchanged for
legacy issues.

Stored top-level metadata `claim.require_unblocked: true` opts an issue into the
existing native `IsBlockedInTx` predicate. Both claim transaction bodies read
the canonical pre-image and check the stored boolean before ownership, leases
or events. Missing/false keeps legacy behavior; malformed/null refuses admission.
An existing in-progress holder retains its idempotent retry contract, including
actor spelling normalization, even after a new blocker appears.

## Concurrent native blocking changes

The first sequential prototype did not conflict with concurrent dependency
updates. The two-session characterization at commit86dbcb3 reproduced a staged
claim committing after a blocking dependency. The fix reuses `row_lock`, which
claims already rewrite: every existing native SQL writer marking a real
`is_blocked` 0-to-1 transition now rewrites that cell **only for the stored JSON
boolean opt-in**. No schema, table, lock service, driver change or retry layer
is added. Shared SQL generation covers common direct/prechecked dependency
writes, the repository write, issue/wisp scoped recomputes and full repair.

This opt-in intentionally extends RowVersion semantics: a newly blocking native
dependency invalidates an older claim or ExpectedVersion. Legacy issues,
false/mistyped metadata, no-op refreshes and unmarking keep their previous token;
auxiliary updates still preserve `updated_at`. Do not stamp the token on all
legacy recomputes. The exception and its legacy boundary are documented beside
the original invariant in `internal/storage/issueops/lease.go`.

The deterministic embedded test now requires serialization error1213 after the
blocking transaction commits before a staged claim. Reopening must show an open,
unassigned issue with no started_at, claim event or lease. A fresh retry refuses
the blocker; after the prerequisite closes, claim succeeds. Both claim bodies
are tested against six mutation paths on issue and wisp sources. Separate
ExpectedVersion controls exercise typed enrollment, timestamps, no-op and unmark.

The transactions overlap; the earlier characterization was not evidence of an
ordinary transaction-linearizability violation. This fix proves the native
blocked-marker conflict contract on the real embedded provider. It does not
establish standalone CLI concurrency or SQL-server execution coverage. It does
not stop a dependency from being added after a claim has completed.

## Authority and remaining boundaries

This constraint is not permission, authenticated caller identity, business
registration, an external-registry version permit, immutable enrollment, or an
authoritative scan of all external holds. It inherits the native cached
`is_blocked` predicate. Authorized metadata writers can change enrollment; direct
SQL bypassing native writers is outside this contract. It does not prohibit
status changes outside claim, cover external business actions or enable routing.

Stephanie Jarmak / sjarmak's https://github.com/gastownhall/beads/pull/7049 is prior
art for the common claim dependency guard and refused-claim tests. Its newer
global-default/force design is not superseded by this old-pin opt-in adaptation;
its holder-retry review informed compatibility tests. Attribution remains in the
original commit. No upstream branch or PR is changed.

Nontrivial merge still requires substantive human review under
PR_MAINTAINER_GUIDELINES.md. No installed binary, live database, schema, service,
credentials or production routing is modified by this source change.

## Validation commands

Use the canonical test runner with explicit embedded prerequisites:

```bash
BEADS_TEST_EMBEDDED_DOLT=1 BEADS_TEST_ENV_RUN_DOLT=1 TEST_VERBOSE=1 \
  ./scripts/test.sh -run '^TestEmbeddedClaimReadiness' ./internal/storage/embeddeddolt
```

Build an isolated candidate with normal `make build BUILD_DIR=/absolute/output`
into an existing fixture directory, without installing it. Then run
`python3 scripts/test-claim-unblocked-prototype.py /absolute/output/bd /absolute/fresh-fixture`.
The CLI probe refuses an existing fixture, isolates HOME/repository, invokes the
supplied ELF directly with `--sandbox`, and retains command receipts plus its
SHA256. It checks sequential CLI parsing/persistence, remaining-blocker refusal,
legacy behavior and holder retry. The two-transaction test covers the distinct
concurrent storage boundary.
