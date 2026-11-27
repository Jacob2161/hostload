package hostload_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jacob2161/hostload"
)

func ExampleReader() {
	reader := hostload.NewReader()
	var minute hostload.Minute
	// CPU use is measured between two readings, so the first has none.
	minute.Add(reader.Read(time.Now()))
	minute.Add(reader.Read(time.Now().Add(time.Second)))
	summary := minute.Close()
	if summary.CPUAverage != nil {
		fmt.Printf(
			"CPU %.0f%% on average, %.0f%% at most\n",
			*summary.CPUAverage*100,
			*summary.CPUMaximum*100,
		)
	}
	if limit := hostload.MemoryLimit(); limit > 0 {
		fmt.Printf("this process may use %d MiB\n", limit>>20)
	}
}

func BenchmarkRead(b *testing.B) {
	reader := hostload.NewReader()
	now := time.Now()
	for b.Loop() {
		now = now.Add(time.Second)
		reader.Read(now)
	}
}
