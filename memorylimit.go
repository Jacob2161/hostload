package hostload

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// systemFiles names the files the readers read; tests point them at
// temporary files.
type systemFiles struct {
	// processCgroup is /proc/self/cgroup, whose "0::" line names the
	// process's cgroup v2 path.
	processCgroup string
	// mountInfo is /proc/self/mountinfo, whose cgroup2 line at cgroupRoot
	// says which cgroup the mount shows as its root: a container without its
	// own cgroup namespace may see only a subtree, at a path that does not
	// start at the hierarchy's root.
	mountInfo string
	// cgroupRoot is where the cgroup v2 file system is mounted.
	cgroupRoot string
	// memoryInfo is /proc/meminfo, whose MemTotal is the machine's memory.
	memoryInfo string
}

// linuxFiles are the files of a Linux system.
var linuxFiles = systemFiles{
	processCgroup: "/proc/self/cgroup",
	mountInfo:     "/proc/self/mountinfo",
	cgroupRoot:    "/sys/fs/cgroup",
	memoryInfo:    "/proc/meminfo",
}

// machineMemoryLimit reads the memory limit once per process.
var machineMemoryLimit = sync.OnceValue(func() int64 {
	return readMemoryLimit(linuxFiles)
})

// MemoryLimit is how many bytes of memory this process may use: the
// smallest memory.max of its cgroup and the cgroups above it, or the
// machine's memory when that is lower or no limit is set; 0 when neither is
// known, as on macOS. It is read once per process.
func MemoryLimit() int64 {
	return machineMemoryLimit()
}

// readMemoryLimit returns the smallest finite memory.max on the path from the
// process's cgroup up to the mount's root, capped at MemTotal, or 0 when
// neither is known.
func readMemoryLimit(files systemFiles) int64 {
	memory := memoryTotal(files.memoryInfo)
	cgroupLimit := cgroupMemoryLimit(files)
	if cgroupLimit > 0 && (memory == 0 || cgroupLimit < memory) {
		memory = cgroupLimit
	}
	return memory
}

// memoryTotal reads MemTotal from /proc/meminfo in bytes, or 0.
func memoryTotal(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		memoryColumns := strings.Fields(lines.Text())
		if len(memoryColumns) != 3 || memoryColumns[0] != "MemTotal:" || memoryColumns[2] != "kB" {
			continue
		}
		kibibytes, err := strconv.ParseInt(memoryColumns[1], 10, 64)
		if err != nil || kibibytes <= 0 {
			return 0
		}
		return kibibytes << 10
	}
	return 0
}

// cgroupMemoryLimit returns the smallest finite memory.max from the
// process's cgroup v2 directory up to the mount's root, or 0 when none is
// set, the process is in no cgroup v2, or its cgroup lies outside the
// mounted subtree.
func cgroupMemoryLimit(files systemFiles) int64 {
	directory, found := cgroupDirectory(files)
	if !found {
		return 0
	}
	root := filepath.Clean(files.cgroupRoot)
	lowest := int64(0)
	for {
		limit := memoryMax(filepath.Join(directory, "memory.max"))
		if limit > 0 && (lowest == 0 || limit < lowest) {
			lowest = limit
		}
		if directory == root || !strings.HasPrefix(directory, root) {
			return lowest
		}
		directory = filepath.Dir(directory)
	}
}

// cgroupDirectory returns the process's cgroup v2 directory under the
// mounted cgroup file system, and false when the process is in no cgroup v2
// or its cgroup lies outside the mounted subtree.
func cgroupDirectory(files systemFiles) (string, bool) {
	cgroupPath, err := unifiedCgroup(files.processCgroup)
	if err != nil {
		return "", false
	}
	root := filepath.Clean(files.cgroupRoot)
	relative, inside := mountedCgroup(cgroupPath, cgroupMountRoot(files.mountInfo, root))
	if !inside {
		return "", false
	}
	return filepath.Join(root, relative), true
}

// cgroupMountRoot returns the cgroup the cgroup2 mount at mountpoint shows
// as its root, from /proc/self/mountinfo, or "/" when the file or the mount
// is missing, as when the whole hierarchy is mounted.
func cgroupMountRoot(mountInfo string, mountpoint string) string {
	contents, err := os.ReadFile(mountInfo)
	if err != nil {
		return "/"
	}
	for line := range bytes.Lines(contents) {
		// The fields are: mount ID, parent ID, major:minor, root,
		// mountpoint, mount flags, optional fields, "-", the file system
		// type, the source, and super block flags.
		mountFields, typeFields, found := strings.Cut(strings.TrimSpace(string(line)), " - ")
		if !found {
			continue
		}
		mountColumns := strings.Fields(mountFields)
		typeColumns := strings.Fields(typeFields)
		if len(mountColumns) < 5 || len(typeColumns) == 0 || typeColumns[0] != "cgroup2" {
			continue
		}
		if filepath.Clean(unescapeMountField(mountColumns[4])) == mountpoint {
			return filepath.Clean(unescapeMountField(mountColumns[3]))
		}
	}
	return "/"
}

// unescapeMountField undoes the octal escapes mountinfo writes for a space,
// a tab, a newline, and a backslash.
func unescapeMountField(field string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).
		Replace(field)
}

// mountedCgroup returns the cgroup's path under a mount whose root is
// mountRoot, and false when the cgroup lies outside it.
func mountedCgroup(cgroupPath string, mountRoot string) (string, bool) {
	if mountRoot == "/" {
		return cgroupPath, true
	}
	if cgroupPath == mountRoot {
		return "/", true
	}
	relative, found := strings.CutPrefix(cgroupPath, mountRoot+"/")
	if !found {
		return "", false
	}
	return "/" + relative, true
}

// unifiedCgroup returns the cgroup v2 path in /proc/self/cgroup: the
// "0::" line.
func unifiedCgroup(processCgroup string) (string, error) {
	contents, err := os.ReadFile(processCgroup)
	if err != nil {
		return "", err
	}
	for line := range bytes.Lines(contents) {
		cgroupPath, found := strings.CutPrefix(strings.TrimSpace(string(line)), "0::")
		if found {
			return filepath.Clean("/" + cgroupPath), nil
		}
	}
	return "", errors.New("no cgroup v2 path")
}

// memoryMax reads one memory.max: a byte count, or 0 for "max" or a
// missing file.
func memoryMax(path string) int64 {
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(contents)), 10, 64)
	if err != nil || limit <= 0 {
		return 0
	}
	return limit
}
