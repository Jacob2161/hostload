package hostload

import (
	"os"
	"path/filepath"
	"testing"
)

// memoryFiles lays out a fake /proc/self/cgroup, cgroup file system, and
// /proc/meminfo in a temporary directory.
type memoryFiles struct {
	t     *testing.T
	files systemFiles
}

// newMemoryFiles writes the process's cgroup line and MemTotal; an empty
// memoryTotal writes no /proc/meminfo.
func newMemoryFiles(t *testing.T, cgroupLine string, memoryTotal string) *memoryFiles {
	t.Helper()
	directory := t.TempDir()
	files := &memoryFiles{t: t, files: systemFiles{
		processCgroup: filepath.Join(directory, "cgroup"),
		mountInfo:     filepath.Join(directory, "mountinfo"),
		cgroupRoot:    filepath.Join(directory, "sys", "fs", "cgroup"),
		memoryInfo:    filepath.Join(directory, "meminfo"),
	}}
	files.write(files.files.processCgroup, cgroupLine)
	if memoryTotal != "" {
		files.write(
			files.files.memoryInfo,
			"MemTotal:       "+memoryTotal+" kB\nMemFree:         1000 kB\n",
		)
	}
	return files
}

// write creates a file and its directories.
func (f *memoryFiles) write(path string, contents string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// limit writes memory.max in a cgroup directory under the root.
func (f *memoryFiles) limit(cgroupPath string, memoryMax string) {
	f.t.Helper()
	f.write(filepath.Join(f.files.cgroupRoot, cgroupPath, "memory.max"), memoryMax+"\n")
}

// TestMemoryLimit checks the cgroup limits and MemTotal.
func TestMemoryLimit(t *testing.T) {
	const gibibyte = int64(1) << 30
	for _, memoryCase := range []struct {
		name string
		// layout writes the files.
		layout func() *memoryFiles
		want   int64
	}{
		{
			name: "no limit, MemTotal",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/app.slice/server.service\n", "4194304")
				files.limit("app.slice/server.service", "max")
				return files
			},
			want: 4 * gibibyte,
		},
		{
			name: "own limit",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/app.slice/server.service\n", "4194304")
				files.limit("app.slice/server.service", "2147483648")
				return files
			},
			want: 2 * gibibyte,
		},
		{
			name: "parent limit under an unlimited child",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/app.slice/server.service\n", "4194304")
				files.limit("app.slice", "1073741824")
				files.limit("app.slice/server.service", "max")
				return files
			},
			want: gibibyte,
		},
		{
			name: "container root limit",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/\n", "4194304")
				files.limit("", "1610612736")
				return files
			},
			want: 1536 << 20,
		},
		{
			name: "subtree mount without a cgroup namespace",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/tenant/job\n", "4194304")
				files.mount("/tenant")
				// The mount shows /tenant at its root: the job's own limit
				// is at job/memory.max, not tenant/job/memory.max.
				files.limit("", "8589934592")
				files.limit("job", "2147483648")
				return files
			},
			want: 2 * gibibyte,
		},
		{
			name: "cgroup outside the mounted subtree",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/elsewhere/job\n", "1048576")
				files.mount("/tenant")
				files.limit("", "536870912")
				return files
			},
			want: gibibyte,
		},
		{
			name: "limit above MemTotal",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/server\n", "1048576")
				files.limit("server", "8589934592")
				return files
			},
			want: gibibyte,
		},
		{
			name: "cgroup v1 only",
			layout: func() *memoryFiles {
				return newMemoryFiles(t, "4:memory:/server\n", "1048576")
			},
			want: gibibyte,
		},
		{
			name: "missing files",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/server\n", "")
				if err := os.Remove(files.files.processCgroup); err != nil {
					t.Fatal(err)
				}
				return files
			},
			want: 0,
		},
		{
			name: "limit without MemTotal",
			layout: func() *memoryFiles {
				files := newMemoryFiles(t, "0::/server\n", "")
				files.limit("server", "536870912")
				return files
			},
			want: 512 << 20,
		},
	} {
		t.Run(memoryCase.name, func(t *testing.T) {
			files := memoryCase.layout()
			if got := readMemoryLimit(files.files); got != memoryCase.want {
				t.Fatalf("readMemoryLimit = %d, want %d", got, memoryCase.want)
			}
		})
	}
}

// mount writes a mountinfo with a cgroup2 mount at the cgroup root that
// shows mountRoot as its root, beside an unrelated mount.
func (f *memoryFiles) mount(mountRoot string) {
	f.t.Helper()
	f.write(f.files.mountInfo, "22 1 0:21 / / rw,relatime - ext4 /dev/root rw\n"+
		"35 22 0:30 "+mountRoot+" "+f.files.cgroupRoot+
		" rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate\n")
}
