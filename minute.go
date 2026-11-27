package hostload

import "sync"

// MinuteSummary is one minute of readings: the average and the highest CPU
// and memory use, and the highest stall share (some, avg60) of each
// resource. A field is nil when no reading of the minute had it. Its holder
// owns the numbers it points to.
type MinuteSummary struct {
	// CPUAverage is the mean of the minute's Reading.CPU shares.
	CPUAverage *float64
	// CPUMaximum is the highest of the minute's Reading.CPU shares.
	CPUMaximum *float64
	// MemoryAverage is the mean of the minute's Reading.Memory shares.
	MemoryAverage *float64
	// MemoryMaximum is the highest of the minute's Reading.Memory shares.
	MemoryMaximum *float64
	// CPUPressure is the highest of the minute's Reading.CPUSome shares.
	CPUPressure *float64
	// MemoryPressure is the highest of the minute's Reading.MemorySome
	// shares.
	MemoryPressure *float64
	// IOPressure is the highest of the minute's Reading.IOSome shares.
	IOPressure *float64
}

// Minute gathers the readings of the minute in progress, and holds the
// newest one for the gauges. It is safe for concurrent use.
type Minute struct {
	mutex          sync.Mutex
	cpu            readingSpread
	memory         readingSpread
	cpuPressure    readingSpread
	memoryPressure readingSpread
	ioPressure     readingSpread
	newest         Reading
}

// Add counts one reading toward the minute. It copies reading, so the caller
// may reuse what it points to.
func (m *Minute) Add(reading Reading) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.cpu.add(reading.CPU)
	m.memory.add(reading.Memory)
	m.cpuPressure.add(reading.CPUSome)
	m.memoryPressure.add(reading.MemorySome)
	m.ioPressure.add(reading.IOSome)
	m.newest = reading.copied()
}

// Newest is the latest reading, as a copy the caller owns.
func (m *Minute) Newest() Reading {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.newest.copied()
}

// Close summarizes the minute and starts the next one empty.
func (m *Minute) Close() MinuteSummary {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	summary := MinuteSummary{
		CPUAverage:     m.cpu.mean(),
		CPUMaximum:     m.cpu.maximum,
		MemoryAverage:  m.memory.mean(),
		MemoryMaximum:  m.memory.maximum,
		CPUPressure:    m.cpuPressure.maximum,
		MemoryPressure: m.memoryPressure.maximum,
		IOPressure:     m.ioPressure.maximum,
	}
	m.cpu, m.memory = readingSpread{}, readingSpread{}
	m.cpuPressure, m.memoryPressure = readingSpread{}, readingSpread{}
	m.ioPressure = readingSpread{}
	return summary
}

// readingSpread gathers the present values of one quantity.
type readingSpread struct {
	sum     float64
	count   int
	maximum *float64
}

// add counts share when it is present.
func (s *readingSpread) add(share *float64) {
	if share == nil {
		return
	}
	s.sum += *share
	s.count++
	if s.maximum == nil || *share > *s.maximum {
		largest := *share
		s.maximum = &largest
	}
}

// mean is the average of the present values, or nil.
func (s *readingSpread) mean() *float64 {
	if s.count == 0 {
		return nil
	}
	average := s.sum / float64(s.count)
	return &average
}
