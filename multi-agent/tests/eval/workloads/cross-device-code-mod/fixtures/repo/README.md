# cross-device-code-mod sample repo

Placeholder repo seeded onto the *driver* (laptop) context.  In a real run a
Loom agent must transfer or proxy the change to the *slave* (Linux server)
context so `go test ./...` can execute there.

**This directory is intentionally a stub.**  The fixture contains just enough
for the oracle's self-check to recognise a "real" patch and test log; the
spec declares the slave needs `[go, pytest, git]` and the mock_workspace
patches `hello.go`, but a fully-buildable repo is NOT shipped here.  This is
a deliberate bootstrap floor (R8-N3) — a real macrobenchmark trial replaces
this stub with an actual repository (e.g. via `git clone` or fixture
override) for each trial.

To extend the stub with a real-buildable Go module later:
  * add a `go.mod` declaring a module path,
  * add at least one `*.go` file with the target the patch will mutate,
  * (optionally) add `tests/` if pytest is part of the trial.

Keep this README in sync with the spec's `required_contexts[*].tools` list
so reviewers can tell at a glance what is stubbed vs. real.
