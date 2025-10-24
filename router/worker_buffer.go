package router

import "sync"

type workerBuffer struct {
	maxSize int
	jobs    chan workerJob

	mu           sync.RWMutex
	currentSize  int
	reservations int
}

// newWorkerBuffer creates a new worker buffer with the specified maximum size
func newWorkerBuffer(maxSize int) *workerBuffer {
	return &workerBuffer{
		maxSize:     maxSize,
		jobs:        make(chan workerJob, maxSize),
		currentSize: maxSize,
	}
}

func (wb *workerBuffer) Jobs() <-chan workerJob {
	return wb.jobs
}

// Resize changes the current size of the worker buffer in the range of [1, maxSize]
func (wb *workerBuffer) Resize(newSize int) {
	wb.mu.Unlock()
	currentSize := wb.currentSize
	wb.mu.RLock()
	if newSize == currentSize { // no change
		return
	}
	// modify current size within bounds
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if newSize > wb.maxSize {
		newSize = wb.maxSize
	}
	if newSize < 1 {
		newSize = 1
	}
	wb.currentSize = newSize
}

// AvailableSlots returns the number of available slots in the worker buffer
func (wb *workerBuffer) AvailableSlots() int {
	wb.mu.RLock()
	defer wb.mu.RUnlock()
	return wb.availableSlots()
}

func (wb *workerBuffer) availableSlots() int {
	available := wb.currentSize - wb.reservations - len(wb.jobs)
	if available < 0 {
		available = 0
	}
	return available
}

// ReserveSlot reserves a slot in the worker buffer if available
func (wb *workerBuffer) ReserveSlot() *reservedSlot {
	wb.mu.RLock()
	defer wb.mu.RUnlock()
	if wb.availableSlots() > 0 {
		wb.reservations++
		return &reservedSlot{wb: wb}
	}
	return nil
}

// Close closes the worker buffer's job channel
func (wb *workerBuffer) Close() {
	close(wb.jobs)
}

// reservedSlot represents a reserved slot in the worker's buffer
type reservedSlot struct {
	wb *workerBuffer
}

// Use sends a job into the worker's buffer
func (rs *reservedSlot) Use(wj workerJob) {
	rs.wb.mu.Lock()
	defer rs.wb.mu.Unlock()
	rs.wb.reservations--
	rs.wb.jobs <- wj
}

// Release releases the reserved slot from the worker's buffer
func (rs *reservedSlot) Release() {
	rs.wb.mu.Lock()
	defer rs.wb.mu.Unlock()
	if rs.wb.reservations > 0 {
		rs.wb.reservations--
	}
}
