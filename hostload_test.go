package hostload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFiles writes each file under directory.
func writeFiles(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestReadMachine reads CPU between two readings, memory, and pressure from
// /proc when there is no cgroup.
func TestReadMachine(t *testing.T) {
	proc := t.TempDir()
	writeFiles(t, proc, map[string]string{
		"stat":            "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3\n",
		"meminfo":         "MemTotal:       1000 kB\nMemFree:  100 kB\nMemAvailable:    250 kB\n",
		"pressure/cpu":    "some avg10=1.00 avg60=12.50 avg300=3.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"pressure/memory": "some avg10=0.00 avg60=4.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=2.00 avg300=0.00 total=0\n",
	})
	reader := newReader(proc, "", "")
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first := reader.Read(start)
	if first.CPU != nil {
		t.Fatalf("the first reading has CPU use %v", *first.CPU)
	}
	if first.Memory == nil || *first.Memory != 0.75 {
		t.Fatalf("memory %v, want 0.75", first.Memory)
	}
	if first.CPUSome == nil || *first.CPUSome != 0.125 || first.MemoryFull == nil ||
		*first.MemoryFull != 0.02 {
		t.Fatalf("pressure: %+v", first)
	}
	if first.IOSome != nil || first.IOFull != nil {
		t.Fatal("IO pressure without its file is not nil")
	}
	// 100 more busy and 100 more idle: half busy.
	writeFiles(t, proc, map[string]string{"stat": "cpu  150 0 150 750 150 0 0 0 0 0\n"})
	second := reader.Read(start.Add(10 * time.Second))
	if second.CPU == nil || *second.CPU != 0.5 {
		t.Fatalf("CPU %v, want 0.5", second.CPU)
	}
}

// TestReadCgroup reads shares of the cgroup's limits, not the machine's.
func TestReadCgroup(t *testing.T) {
	proc := t.TempDir()
	cgroup := t.TempDir()
	writeFiles(t, proc, map[string]string{
		"stat":    "cpu  100 0 100 700 100 0 0 0 0 0\n",
		"meminfo": "MemTotal: 1000 kB\nMemAvailable: 900 kB\n",
	})
	writeFiles(t, cgroup, map[string]string{
		"cgroup.controllers": "cpu memory io\n",
		"cpu.stat":           "usage_usec 1000000\nuser_usec 1\n",
		"cpu.max":            "50000 100000\n",
		"memory.max":         "1000\n",
		"memory.current":     "600\n",
		"memory.stat":        "anon 300\ninactive_file 200\n",
		"io.pressure":        "some avg10=0.00 avg60=40.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=10.00 avg300=0.00 total=0\n",
	})
	reader := newReader(proc, cgroup, cgroup)
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first := reader.Read(start)
	if first.Memory == nil || *first.Memory != 0.4 {
		t.Fatalf("memory %v, want the working set over memory.max, 0.4", first.Memory)
	}
	if first.IOSome == nil || *first.IOSome != 0.4 || first.IOFull == nil || *first.IOFull != 0.1 {
		t.Fatalf("IO pressure: %+v", first)
	}
	// Half a CPU limit, used for 2.5 s over 10 s: half the limit.
	writeFiles(t, cgroup, map[string]string{"cpu.stat": "usage_usec 3500000\n"})
	second := reader.Read(start.Add(10 * time.Second))
	if second.CPU == nil || *second.CPU != 0.5 {
		t.Fatalf("CPU %v, want 0.5 of the cgroup's limit", second.CPU)
	}
}

// TestReadNothing reads a system with none of the files as absent values.
func TestReadNothing(t *testing.T) {
	reader := newReader(t.TempDir(), "", "")
	reading := reader.Read(time.Now())
	if reading != (Reading{}) {
		t.Fatalf("a system without the files: %+v", reading)
	}
	if ownCgroup(systemFiles{processCgroup: filepath.Join(t.TempDir(), "cgroup"), cgroupRoot: t.TempDir()}) != "" {
		t.Fatal("a cgroup without /proc/self/cgroup")
	}
}

// TestOwnCgroup finds this process's cgroup v2 directory, including under a
// mount that shows only a subtree of the hierarchy.
func TestOwnCgroup(t *testing.T) {
	proc := t.TempDir()
	root := t.TempDir()
	writeFiles(t, proc, map[string]string{"self/cgroup": "0::/system.slice/app.service\n"})
	writeFiles(t, root, map[string]string{"system.slice/app.service/cgroup.controllers": "cpu\n"})
	files := systemFiles{
		processCgroup: filepath.Join(proc, "self", "cgroup"),
		mountInfo:     filepath.Join(proc, "self", "mountinfo"),
		cgroupRoot:    root,
	}
	if got := ownCgroup(files); got != filepath.Join(root, "system.slice/app.service") {
		t.Fatalf("own cgroup %q", got)
	}

	// Without its own cgroup namespace, a container sees /tenant at the
	// mount's root, so its cgroup /tenant/job is at job, not tenant/job.
	writeFiles(t, proc, map[string]string{
		"self/cgroup":    "0::/tenant/job\n",
		"self/mountinfo": "30 25 0:26 /tenant " + root + " rw,nosuid - cgroup2 cgroup2 rw\n",
	})
	writeFiles(t, root, map[string]string{"job/cgroup.controllers": "cpu\n"})
	if got := ownCgroup(files); got != filepath.Join(root, "job") {
		t.Fatalf("own cgroup under a subtree mount %q", got)
	}
}

// TestMinuteClose averages and maximizes a minute's readings and starts the
// next minute empty.
func TestMinuteClose(t *testing.T) {
	low, high := 0.2, 0.6
	var minute Minute
	minute.Add(Reading{CPU: &low, IOSome: &low})
	minute.Add(Reading{CPU: &high, IOSome: &high})
	summary := minute.Close()
	if summary.CPUAverage == nil || *summary.CPUAverage != 0.4 || *summary.CPUMaximum != 0.6 ||
		*summary.IOPressure != 0.6 || summary.MemoryAverage != nil {
		t.Fatalf("summary: %+v", summary)
	}
	if next := minute.Close(); next.CPUAverage != nil || next.IOPressure != nil {
		t.Fatalf("the next minute: %+v", next)
	}
	if newest := minute.Newest(); newest.CPU == nil || *newest.CPU != high {
		t.Fatal("the newest reading is not kept")
	}
}

// TestReadInheritedLimits reads shares of a parent cgroup's limits when the
// process's own cgroup sets none.
func TestReadInheritedLimits(t *testing.T) {
	proc := t.TempDir()
	root := t.TempDir()
	writeFiles(t, proc, map[string]string{"meminfo": "MemTotal: 1000000 kB\nMemAvailable: 900000 kB\n"})
	writeFiles(t, root, map[string]string{
		"tenant/cpu.max":                "100000 100000\n",
		"tenant/memory.max":             "1000\n",
		"tenant/job/cgroup.controllers": "cpu memory\n",
		"tenant/job/cpu.max":            "max 100000\n",
		"tenant/job/memory.max":         "max\n",
		"tenant/job/memory.current":     "500\n",
		"tenant/job/cpu.stat":           "usage_usec 0\n",
	})
	reader := newReader(proc, filepath.Join(root, "tenant", "job"), root)
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first := reader.Read(start)
	if first.Memory == nil || *first.Memory != 0.5 {
		t.Fatalf("memory %v, want 0.5 of the parent's 1000 bytes", first.Memory)
	}
	// One core from the parent, used for 5 s over 10 s: half.
	writeFiles(t, root, map[string]string{"tenant/job/cpu.stat": "usage_usec 5000000\n"})
	if second := reader.Read(start.Add(10 * time.Second)); second.CPU == nil || *second.CPU != 0.5 {
		t.Fatalf("CPU %v, want 0.5 of the parent's one core", second.CPU)
	}
}

// TestQuotaChangeKeepsMeasuring changes the CPU limit between readings: the
// share is of the elapsed time at the new limit, not skewed by it.
func TestQuotaChangeKeepsMeasuring(t *testing.T) {
	proc := t.TempDir()
	cgroup := t.TempDir()
	writeFiles(t, cgroup, map[string]string{
		"cgroup.controllers": "cpu\n",
		"cpu.stat":           "usage_usec 0\n",
		"cpu.max":            "100000 100000\n",
	})
	reader := newReader(proc, cgroup, cgroup)
	reader.cpus = 8
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader.Read(start)
	// Two cores now, used for 10 s over 10 s: half.
	writeFiles(t, cgroup, map[string]string{"cpu.max": "200000 100000\n", "cpu.stat": "usage_usec 10000000\n"})
	if reading := reader.Read(start.Add(10 * time.Second)); reading.CPU == nil || *reading.CPU != 0.5 {
		t.Fatalf("CPU %v after the quota doubled, want 0.5", reading.CPU)
	}
}
