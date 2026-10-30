# Tests

`make verify` runs formatting, architecture/module checks, vet, library and CLI
tests, race detection and builds. Tests use temporary repositories and local bare
remotes, without production credentials or network services. Rust is not needed.

`tests/cli/` exercises the executable as a separate process. Scenarios cover
structured output, authoring replay, concurrent claims, assignments, handoffs,
worktrees, canonical history/completion and ambiguous writes. Its 32 accept
reconciliation cases inject worker changes, pending handoffs, absent refs,
unreadable/malformed responses and unchanged assignments before receipt writes.
The Git wrapper under testdata/ is the retained fault-injection fixture.
Terminal-color tests use the platform `script` utility to exercise a real
pseudoterminal. The Linux development shell supplies it; macOS includes it.

```sh
make test-cli                     # focused CLI suite; also in make verify
make fuzz                         # three bounded parser campaigns
make security                     # pinned scanner; requires vulnerability DB access
```

The CLI suite builds its executable once in a temporary directory. With
`go test -race`, that executable is also race-instrumented. For artifact checks,
set `TSUZURI_TEST_BINARY` to an absolute executable path; its instrumentation is
then the caller's responsibility. The Make gates disable test caching so CLI
source changes always exercise a rebuilt executable. Subprocesses have deadlines,
bounded output and process-group cancellation. Fixtures isolate Git configuration
and identity. The per-package test budget is five minutes for the full CLI suite
on hosted macOS runners; individual test subprocesses remain bounded to one minute.

The scenarios derive from the Apache-2.0 Tsuzuri v0.3.1 CLI tests at
`34d55cbc473992045c9608f35f62e69e8a4e9db0`, ported from this repository's
`e9b97f10ac6f88577d5e4ec8642de2a42db0be88` test snapshot. The port retains the
assertions and failure-injection script; no reference implementation is built.

Optional mixed-client checks accept a separately supplied reference executable:

```sh
TSUZURI_RUST_BINARY=/absolute/path/to/reference-v0.3.1 make interop
```

These checks explicitly skip when no binary is supplied. They are supplementary
evidence, not a Rust build requirement or a substitute for Go regressions.
`scripts/reproduce-stale-worker.sh` is a local-only regression reproduction.

`history_recovery_test.go` covers corrected terminal sections, retained lifecycle
and frontmatter refusals, strict original snapshots, diagnostic locations, and
exact-worker recovery across independent clones. To compare the deliberate
recovery difference with rc.4, set `TSUZURI_HISTORY_BASELINE` to that executable
and run `go test -count=1 -run 'TestCorrectedTerminalHistoryRecovery' .`.
That test expects rc.4 and the optional v0.3.1 reference to reject the repaired
history; the candidate must accept it without replacing existing ownership.
The other historical replay cases retain their existing expectations.
`TestCorrectedRegistryOrderHistoryRecovery` covers corrected `areas`, `risks`
and `blocked_by` order the same way; with `TSUZURI_HISTORY_BASELINE` set to
rc.6 (which still rejects corrected `blocked_by`), or `TSUZURI_RUST_BINARY` set
to a Rust reference, it expects that executable to reject the repaired history.

Tests target Linux and macOS. Cross-builds do not prove native execution or real
credential-helper behavior. Record omitted platforms and checks honestly. Fuzz
seed runs are not fuzz campaigns; module verification is not vulnerability scanning.

The immutable-read regressions cover per-revision validation, strict/legacy
parsing, cancellation, mutable refs, cache limits and retained malformed claims.
For an optional local timing/process-count comparison, build the candidate and
reuse the history probe with a synthetic many-claim fixture (maximum 200):

```sh
make build
TSUZURI_HISTORY_PROBE=1 TSUZURI_HISTORY_MODE=claims TSUZURI_HISTORY_SIZE=50 \
  TSUZURI_HISTORY_BASELINE=/absolute/path/to/rc3-binary \
  TSUZURI_HISTORY_CANDIDATE="$PWD/bin/tsuzuri" \
  go test -v -count=1 -timeout 300s -run '^TestHistoryProbe$' .
```

This fixture includes completed tasks, a malformed claim and a malformed
assignment. The probe checks identical Go overview JSON data across binaries and
trials, excluding the per-invocation source envelope. Timings are observations,
not test thresholds; wrapper overhead and host load affect them.
