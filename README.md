# hostload

[![Go Reference](https://pkg.go.dev/badge/github.com/jacob2161/hostload.svg)](https://pkg.go.dev/github.com/jacob2161/hostload)
[![CI](https://github.com/jacob2161/hostload/actions/workflows/ci.yml/badge.svg)](https://github.com/jacob2161/hostload/actions/workflows/ci.yml)

How busy the machine is, as your process sees it: CPU and memory use and pressure stall information (PSI) from cgroup v2 or /proc, the memory your container may use, and one-minute summaries.

```bash
go get github.com/jacob2161/hostload
```

## Usage

```go
reader := hostload.NewReader()
var minute hostload.Minute
for now := range time.Tick(5 * time.Second) {
	minute.Add(reader.Read(now))
	if now.Second() < 5 {
		summary := minute.Close() // averages and maxima, nil when unknown
		record(summary)
	}
}

limit := hostload.MemoryLimit() // cgroup memory.max, capped at MemTotal
```

- In a container, shares are of the container's cgroup limits, not the host's, including when the cgroup mount shows only a subtree of the hierarchy (no cgroup namespace).
- It only reads files. Values a system does not report (macOS, a kernel without PSI) are nil, never a made-up zero.

Tested on the two newest Go releases. Licensed under the MIT license.
