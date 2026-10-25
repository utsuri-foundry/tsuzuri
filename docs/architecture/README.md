# Source organization

The root `tsuzuri` package is the reusable library. Its files group parsing and
models, Git access, history validation, coordination and inspection. Related
`*_test.go` files stay beside the implementation. `cmd/tsuzuri` is the thin CLI;
`cmd/check-architecture` is a development check. `scripts/` contains packaging
and reproduction utilities, and `tests/cli/` holds black-box CLI tests in Go.
Go module files, the Makefile and optional Nix environment belong at the root.

`architecture.json` records file ownership, allowed imports and source budgets.
The checker enforces these even within the root package; it is not a substitute
for semantic review. Keep the existing package API stable. Introduce an internal
package only when a distinct responsibility and dependency direction justify it,
not to hide the number of source files from the repository listing.

Tasks/configuration are Markdown/TOML; claims, assignments and receipts are Git
objects and refs. Native Git owns transport and conditional updates. Domain
validation does not own execution authority. See [integration](../integration.md)
for cancellation, trust boundaries and recovery invariants.

Each operation may reuse immutable object reads and parsed records. The object
and record caches each admit at most 8 MiB of input and 4,096 entries; immutable
revision resolutions have a 4,096-entry limit. Full caches fall back to ordinary
reads and parsing. These are cache admission limits, not a total process-memory
limit. Mutable refs, errors and revision-specific validation are not cached by
this mechanism, and no cache survives the operation.
