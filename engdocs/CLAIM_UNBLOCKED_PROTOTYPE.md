# Opt-in claim dependency constraint (pinned compatibility prototype)

Journal: https://github.com/AlexBelous/gascity-kb/issues/128

On pinned Beads da08f2739, exact-ID claim accepts an open issue even when a blocking dependency remains. Existing claim behavior intentionally remains unchanged for legacy issues.

The proposed `claim.require_unblocked: true` top-level metadata field opts a stored issue into the native dependency predicate at claim time. The common transaction body and the unit-of-work SQL repository both read the canonical pre-image, inspect the stored field, and call the existing `IsBlockedInTx` before ownership, leases or events are written. No caller flag is required. A present non-boolean or null field refuses admission. Absent/false preserves legacy behavior. An already-held in-progress issue retains its idempotent retry contract, including actor spelling normalization.

This mechanical constraint is not permission, authenticated caller identity, business registration, an external-registry version permit, or an immutable enrollment. Authorized metadata writers can change it. It does not prohibit status changes outside claim, cover external business actions or enable production routing.

## Concurrency boundary

Putting the dependency read and claim in the same SQL transaction is necessary but does not prove final admission against concurrent dependency changes. Pinned `internal/storage/issueops/lease.go` documents Dolt cell merging and deliberately excludes auxiliary `is_blocked` updates from the ownership `row_lock` invariant; `dependencies.go` marks the source without rewriting that token. The prototype must not be represented as an atomic fence for all persistent holds, current registry/package/contract versions, or UNKNOWN authority. A deterministic concurrent-mutation proof and a compatible native conflict contract remain required before that stronger claim or live activation.

Do not remint the global ownership token on every auxiliary recompute merely to hide this gap: the existing contract explicitly protects legitimate expected-version writers from such spurious conflicts.

## Prior art and review

Stephanie Jarmak / sjarmak's https://github.com/gastownhall/beads/pull/7049 is prior art for a dependency guard in the common claim primitive and the associated refused-claim tests. Its global-default/force design and newer API stack are not replaced or rewritten by this old-pin opt-in experiment. Its unresolved idempotent-holder-retry review informs the compatibility test. No upstream branch or PR is changed.

Any nontrivial Beads merge still requires substantive human review under PR_MAINTAINER_GUIDELINES.md. No installed binary, live database, schema, service, credentials or production routing is modified by this prototype.


## Manual CLI boundary probe

Build a candidate with the normal `make build BUILD_DIR=/absolute/candidate-output`
into an existing writable fixture directory; do not install or replace the system
binary. Run `python3 scripts/test-claim-unblocked-prototype.py /absolute/candidate-output/bd /absolute/fresh-fixture`.
The probe refuses an existing fixture path, uses an empty HOME and repository,
invokes the supplied binary directly with `--sandbox`, and retains every command
receipt plus its ELF SHA256. It checks the real CLI metadata parser, persistence
across fresh command processes, the remaining-blocker refusal, the legacy control,
and the existing holder retry after a new dependency. It proves sequential behavior
only; the concurrency and authority boundaries above remain unchanged.

## Deterministic concurrent dependency characterization

`TestEmbeddedClaimReadinessConcurrentDependencyBoundary` characterizes the
limitation on the pinned embedded provider, using two real SQL sessions on one
`OpenSQL` connector. It stages the real common/repository claim transaction,
commits an actual `AddDependencyInTx` in the second transaction, and only then
commits claim. No sleeps, mock engine, production database, or installed binary
are involved. The dependency transaction leaves the ownership `row_lock`
unchanged; a fresh session observes `open / is_blocked=true`, while the claim
transaction still sees its earlier `is_blocked=false` snapshot. Both claims
commit successfully. Reopening the store observes the owner, `in_progress`,
the blocking edge, `is_blocked=true`, and one event for each operation.

The characterization currently passes when that known limitation reproduces.
It is **not** a passing final-admission guard test. Once a compatible native
conflict contract is implemented, replace this expected-bypass assertion with
its refusal/retry contract. These operations overlap: this result does not show
a violation of ordinary transaction linearizability, nor a claim started after
a fully completed dependency update. It establishes that the dependency predicate
can become false before the claim commits. It does not reproduce concurrent
standalone CLI processes or a separate SQL-server backend.

Run the focused characterization with the canonical runner and explicit
embedded prerequisites:

```bash
BEADS_TEST_EMBEDDED_DOLT=1 BEADS_TEST_ENV_RUN_DOLT=1 TEST_VERBOSE=1 \
  ./scripts/test.sh -run '^TestEmbeddedClaimReadinessConcurrentDependencyBoundary$' ./internal/storage/embeddeddolt
```

Remaining Source requirement: a compatible conflict contract between opted-in
claim and every dependency-readiness-changing writer, followed by refusal/retry
and legacy expected-version controls. A shared SQL transaction alone is
insufficient for the stronger commit-time readiness requirement. Do not claim
that baseline cleanup or human review resolves this runtime boundary.
