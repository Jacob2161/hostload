package hostload

import "sync"

// MinuteSummary is one minute of readings: the average and the highest CPU
// and memory use, and the highest stall share (some, avg60) of each
// resource. A value is nil when no reading of the minute had it.
type MinuteSummary struct {
	CPUAverage     *float64
	CPUMaximum     *float64
	MemoryAverage  *float64
	MemoryMaximum  *float64
	CPUPressure    *float64
	MemoryPressure *float64
	IOPressure     *float64
}

// Minute gathers the readings of the minute in progress, and keeps the
// newest one for the gauges.
type Minute struct {
	mutex          sync.Mutex
	cpu            readingSpread
	memory         readingSpread
	cpuPressure    readingSpread
	memoryPressure readingSpread
	ioPressure     readingSpread
	newest         Reading
}

// Add counts one reading toward the minute.
func (m *Minute) Add(reading Reading) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.cpu.add(reading.CPU)
	m.memory.add(reading.Memory)
	m.cpuPressure.add(reading.CPUSome)
	m.memoryPressure.add(reading.MemorySome)
	m.ioPressure.add(reading.IOSome)
	m.newest = reading
}

// Newest is the latest reading.
func (m *Minute) Newest() Reading {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.newest
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
	m.cpuPressure, m.memoryPressure, m.ioPressure = readingSpread{}, readingSpread{}, readingSpread{}
	return summary
}

// readingSpread gathers the present values of one quantity.
type readingSpread struct {
	sum     float64
	count   int
	maximum *float64
}

// add counts value when it is present.
func (s *readingSpread) add(value *float64) {
	if value == nil {
		return
	}
	s.sum += *value
	s.count++
	if s.maximum == nil || *value > *s.maximum {
		largest := *value
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
