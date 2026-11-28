# Changelog

This module follows semantic versioning. Before v1, a minor version may
change the API; each such change says what callers must do.

## v0.2.0 (2026-11-28)

- Requires Go 1.27, the newest Go release; Go 1.26 is no longer
  supported.
- CI runs staticcheck and govulncheck at the versions pinned in
  `tools/go.mod`, a module of its own that importers never see.

## v0.1.1 (2026-11-27)

v0.1.2 tags the same commit.

- `Minute.Add` and `Minute.Newest` copy readings, so a caller may reuse or
  change a reading's numbers without changing what the `Minute` holds.
- Every field of `Reading` and `MinuteSummary` has its own doc comment.
- CI also checks formatting, `go mod tidy -diff`, and `govulncheck`.

No API change.

## v0.1.0 (2026-11-27)

First release.

- `Reader.Read` reports CPU and memory use and pressure stall shares as
  the process sees them, from its own cgroup v2 limits in a container, and
  nil for anything the system does not report.
- `Minute` gathers a minute of readings into averages and maxima.
- `MemoryLimit` reports the memory the process may use: the smallest
  cgroup `memory.max`, capped at the machine's memory.
