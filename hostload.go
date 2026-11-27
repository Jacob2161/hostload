// Package hostload reads how busy the machine is, as the calling process sees
// it: CPU and memory use, pressure stall information, and the memory the
// process may use.
//
// Under cgroup v2 it reads the process's own cgroup, so shares are of the
// container's limits, not the host's, including in a container whose cgroup
// mount shows only a subtree of the hierarchy. It only reads files. On a
// system without them, such as macOS or a kernel without PSI, values are nil
// (or 0 for MemoryLimit), never a made-up zero load.
package hostload

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reading is one look at the machine. Every field is a ratio from 0 to 1,
// nil when the system does not report it. The pressure fields are the share
// of the last minute (avg60) in which some, or all (full), tasks stalled on
// a resource. Read returns new numbers each time and never changes them
// afterwards.
type Reading struct {
	// CPU is the share of the CPU time available to the process that was
	// used since the previous Read.
	CPU *float64
	// Memory is the share of the memory available to the process in use.
	Memory *float64
	// CPUSome is the share of the last minute some tasks stalled on CPU.
	CPUSome *float64
	// CPUFull is the share of the last minute all tasks stalled on CPU.
	CPUFull *float64
	// MemorySome is the share of the last minute some tasks stalled on
	// memory.
	MemorySome *float64
	// MemoryFull is the share of the last minute all tasks stalled on
	// memory.
	MemoryFull *float64
	// IOSome is the share of the last minute some tasks stalled on I/O.
	IOSome *float64
	// IOFull is the share of the last minute all tasks stalled on I/O.
	IOFull *float64
}

// copied returns a Reading with the same numbers that shares no pointer with
// r, so its holder owns it.
func (r Reading) copied() Reading {
	return Reading{
		CPU:        copiedShare(r.CPU),
		Memory:     copiedShare(r.Memory),
		CPUSome:    copiedShare(r.CPUSome),
		CPUFull:    copiedShare(r.CPUFull),
		MemorySome: copiedShare(r.MemorySome),
		MemoryFull: copiedShare(r.MemoryFull),
		IOSome:     copiedShare(r.IOSome),
		IOFull:     copiedShare(r.IOFull),
	}
}

// copiedShare returns a new pointer to *share, or nil.
func copiedShare(share *float64) *float64 {
	if share == nil {
		return nil
	}
	copied := *share
	return &copied
}

// Reader reads the machine's load from a proc and a cgroup file system. It is
// safe for concurrent use.
type Reader struct {
	proc string
	// cgroup is the process's cgroup directory, "" without cgroup v2, and
	// cgroupRoot the mount it lies under; limits are the smallest from
	// cgroup up to cgroupRoot.
	cgroup     string
	cgroupRoot string
	cpus       int

	mutex sync.Mutex
	// The previous CPU counters, to measure use between two readings, and
	// whether they came from the cgroup or /proc/stat.
	previousBusy   float64
	previousTotal  float64
	previousTime   time.Time
	previousCgroup bool
}

// NewReader reads /proc and the process's own cgroup under /sys/fs/cgroup.
func NewReader() *Reader {
	return newReader("/proc", ownCgroup(linuxFiles), linuxFiles.cgroupRoot)
}

// newReader reads proc and the cgroup directory under cgroupRoot, "" without
// one.
func newReader(proc string, cgroup string, cgroupRoot string) *Reader {
	return &Reader{
		proc:       proc,
		cgroup:     cgroup,
		cgroupRoot: filepath.Clean(cgroupRoot),
		cpus:       runtime.NumCPU(),
	}
}

// cgroupPath runs from the process's cgroup directory up to the mount's root.
func (r *Reader) cgroupPath(yield func(string) bool) {
	for directory := r.cgroup; ; directory = filepath.Dir(directory) {
		if !yield(directory) || directory == r.cgroupRoot ||
			!strings.HasPrefix(directory, r.cgroupRoot) {
			return
		}
	}
}

// ownCgroup is this process's cgroup v2 directory, or "" without cgroup v2 or
// when the directory is not mounted where files say.
func ownCgroup(files systemFiles) string {
	directory, found := cgroupDirectory(files)
	if !found {
		return ""
	}
	if _, err := os.Stat(filepath.Join(directory, "cgroup.controllers")); err != nil {
		return ""
	}
	return directory
}

// Read looks at the machine once. CPU use is measured between two calls, so
// the first call reports none.
func (r *Reader) Read(now time.Time) Reading {
	var reading Reading
	reading.CPU = r.readCPU(now)
	reading.Memory = r.readMemory()
	reading.CPUSome, reading.CPUFull = r.readPressure("cpu")
	reading.MemorySome, reading.MemoryFull = r.readPressure("memory")
	reading.IOSome, reading.IOFull = r.readPressure("io")
	return reading
}

// readCPU measures CPU use since the previous call: the cgroup's usage over
// the elapsed time at its effective CPU limit when it has cpu.stat, otherwise
// the machine's busy share from /proc/stat. A change of source starts over.
func (r *Reader) readCPU(now time.Time) *float64 {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	usage, cores, fromCgroup := r.cgroupCPU()
	busy, total := usage, 0.0
	if !fromCgroup {
		var found bool
		busy, total, found = r.procCPU()
		if !found {
			r.previousTime = time.Time{}
			return nil
		}
	}
	previousBusy, previousTotal, previousTime := r.previousBusy, r.previousTotal, r.previousTime
	sameSource := !previousTime.IsZero() && r.previousCgroup == fromCgroup
	r.previousBusy, r.previousTotal, r.previousTime, r.previousCgroup = busy, total, now, fromCgroup
	if !sameSource || busy < previousBusy {
		return nil
	}
	if fromCgroup {
		elapsed := float64(now.Sub(previousTime).Microseconds())
		if elapsed <= 0 || cores <= 0 {
			return nil
		}
		return clampedShare((busy - previousBusy) / (elapsed * cores))
	}
	if total <= previousTotal {
		return nil
	}
	return clampedShare((busy - previousBusy) / (total - previousTotal))
}

// cgroupCPU reads the cgroup's CPU time in microseconds, and the cores it may
// use: the smallest cpu.max quota from its cgroup up to the root, at most the
// machine's processors.
func (r *Reader) cgroupCPU() (float64, float64, bool) {
	if r.cgroup == "" {
		return 0, 0, false
	}
	usage, found := readKeyed(filepath.Join(r.cgroup, "cpu.stat"), "usage_usec")
	if !found {
		return 0, 0, false
	}
	cores := float64(r.cpus)
	for directory := range r.cgroupPath {
		limit, err := os.ReadFile(filepath.Join(directory, "cpu.max"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(limit))
		if len(fields) == 2 && fields[0] != "max" {
			quota, quotaErr := strconv.ParseFloat(fields[0], 64)
			period, periodErr := strconv.ParseFloat(fields[1], 64)
			if quotaErr == nil && periodErr == nil && period > 0 && quota > 0 {
				cores = min(cores, quota/period)
			}
		}
	}
	return usage, cores, true
}

// procCPU reads the machine's busy and total CPU time from /proc/stat:
// everything but idle and iowait is busy.
func (r *Reader) procCPU() (float64, float64, bool) {
	stat, err := os.ReadFile(filepath.Join(r.proc, "stat"))
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := bytes.Cut(stat, []byte("\n"))
	fields := strings.Fields(string(line))
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total, idle float64
	for i, field := range fields[1:] {
		ticks, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return 0, 0, false
		}
		// guest and guest_nice are already counted in user and nice.
		if i >= 8 {
			break
		}
		total += ticks
		if i == 3 || i == 4 {
			idle += ticks
		}
	}
	return total - idle, total, true
}

// readMemory is the cgroup's working set (memory.current less inactive file
// pages) over the smallest memory.max from its cgroup up to the root, when
// one is set, otherwise the machine's memory in use (MemTotal less
// MemAvailable) over MemTotal. Under a parent's limit the share counts this
// cgroup's own use, not its siblings'.
func (r *Reader) readMemory() *float64 {
	if r.cgroup != "" {
		current, currentErr := os.ReadFile(filepath.Join(r.cgroup, "memory.current"))
		used, usedErr := strconv.ParseFloat(strings.TrimSpace(string(current)), 64)
		limit := 0.0
		for directory := range r.cgroupPath {
			if maximum := memoryMax(filepath.Join(directory, "memory.max")); maximum > 0 &&
				(limit == 0 || float64(maximum) < limit) {
				limit = float64(maximum)
			}
		}
		if currentErr == nil && usedErr == nil && limit > 0 {
			inactive, _ := readKeyed(filepath.Join(r.cgroup, "memory.stat"), "inactive_file")
			return clampedShare(max(0, used-inactive) / limit)
		}
	}
	total, totalFound := readKeyed(filepath.Join(r.proc, "meminfo"), "MemTotal:")
	available, availableFound := readKeyed(filepath.Join(r.proc, "meminfo"), "MemAvailable:")
	if !totalFound || !availableFound || total <= 0 {
		return nil
	}
	return clampedShare((total - available) / total)
}

// readPressure reads a resource's some and full avg60 shares, from the
// cgroup when it reports pressure, otherwise from /proc/pressure.
func (r *Reader) readPressure(resource string) (*float64, *float64) {
	var stalls []byte
	err := errors.New("no cgroup")
	if r.cgroup != "" {
		stalls, err = os.ReadFile(filepath.Join(r.cgroup, resource+".pressure"))
	}
	if err != nil {
		stalls, err = os.ReadFile(filepath.Join(r.proc, "pressure", resource))
	}
	if err != nil {
		return nil, nil
	}
	var some, full *float64
	for line := range strings.SplitSeq(string(stalls), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		for _, field := range fields[1:] {
			text, found := strings.CutPrefix(field, "avg60=")
			if !found {
				continue
			}
			percent, err := strconv.ParseFloat(text, 64)
			if err != nil {
				continue
			}
			share := clampedShare(percent / 100)
			switch fields[0] {
			case "some":
				some = share
			case "full":
				full = share
			}
		}
	}
	return some, full
}

// readKeyed reads the number after key on the first line that starts with
// it, from a file of "key value" lines such as cpu.stat or meminfo.
func readKeyed(path string, key string) (float64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != key {
			continue
		}
		number, err := strconv.ParseFloat(fields[1], 64)
		return number, err == nil
	}
	return 0, false
}

// clampedShare clamps share to 0 to 1.
func clampedShare(share float64) *float64 {
	clamped := min(1, max(0, share))
	return &clamped
}
