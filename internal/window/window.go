package window

import (
	"sync"
	"time"
)

type RollingWindow struct {
	mu       sync.RWMutex
	duration time.Duration
	items    []item
	missed   int
}

type item struct {
	timestamp    time.Time
	participated bool
	height       uint64
}

type WindowSnapshot struct {
	Duration time.Duration `json:"duration"`
	Items    []ItemData    `json:"items"`
	Missed   int           `json:"missed"`
}

type ItemData struct {
	Timestamp    time.Time `json:"timestamp"`
	Participated bool      `json:"participated"`
	Height       uint64    `json:"height"`
}

// NewRollingWindow creates a new time-based rolling window.
func NewRollingWindow(duration time.Duration) *RollingWindow {
	if duration <= 0 {
		duration = 1 * time.Hour // default fallback
	}
	return &RollingWindow{
		duration: duration,
		items:    make([]item, 0, 1000), // initial capacity
	}
}

// Add records a new block participation with its timestamp.
// It automatically prunes blocks older than the window duration.
func (w *RollingWindow) Add(participated bool, timestamp time.Time, height uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Add new item
	w.items = append(w.items, item{
		timestamp:    timestamp,
		participated: participated,
		height:       height,
	})

	if !participated {
		w.missed++
	}

	// Prune old items
	cutoff := timestamp.Add(-w.duration)
	pruneCount := 0
	for _, it := range w.items {
		if it.timestamp.After(cutoff) {
			break
		}
		// Item is expired
		if !it.participated {
			w.missed--
		}
		pruneCount++
	}

	if pruneCount > 0 {
		// Slice off the pruned items
		// Optimized: If pruneCount is large, we might want to reallocate to avoid memory leak,
		// but for a rolling window, the buffer is likely to be reused or stable.
		w.items = w.items[pruneCount:]
	}
}

// GetStats returns the current missed count, total blocks in window, and missing ratio.
func (w *RollingWindow) GetStats() (missed int, total int, ratio float64) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	total = len(w.items)
	if total == 0 {
		return 0, 0, 0.0
	}
	return w.missed, total, float64(w.missed) / float64(total)
}

// GetBitmap returns the participation status of blocks in the current window.
// Returns a boolean slice where true = participated, false = missed.
// The order is from oldest to newest.
func (w *RollingWindow) GetBitmap() []bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make([]bool, len(w.items))
	for i, it := range w.items {
		result[i] = it.participated
	}
	return result
}

// GetLastParticipation returns the most recent participation entry.
// ok is false if there is no data.
func (w *RollingWindow) GetLastParticipation() (participated bool, height uint64, timestamp time.Time, ok bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.items) == 0 {
		return false, 0, time.Time{}, false
	}

	last := w.items[len(w.items)-1]
	return last.participated, last.height, last.timestamp, true
}

// GetLastSignedMissedHeights returns the most recent signed and missed block heights.
// If no signed or missed entries exist, the corresponding flag is false.
func (w *RollingWindow) GetLastSignedMissedHeights() (lastSigned uint64, hasSigned bool, lastMissed uint64, hasMissed bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for i := len(w.items) - 1; i >= 0; i-- {
		item := w.items[i]
		if item.participated {
			if !hasSigned {
				lastSigned = item.height
				hasSigned = true
			}
		} else {
			if !hasMissed {
				lastMissed = item.height
				hasMissed = true
			}
		}

		if hasSigned && hasMissed {
			break
		}
	}

	return lastSigned, hasSigned, lastMissed, hasMissed
}

// GetLastBlockInterval returns the time between the last two blocks.
// Returns 0 if there are fewer than two blocks.
func (w *RollingWindow) GetLastBlockInterval() time.Duration {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.items) < 2 {
		return 0
	}

	last := w.items[len(w.items)-1].timestamp
	prev := w.items[len(w.items)-2].timestamp
	return last.Sub(prev)
}

// GetAvgBlockTimeLastN calculates the average time between blocks over the last N blocks.
// Returns 0 if there are fewer than two blocks.
func (w *RollingWindow) GetAvgBlockTimeLastN(n int) time.Duration {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.items) < 2 || n < 2 {
		return 0
	}

	start := len(w.items) - n
	if start < 0 {
		start = 0
	}

	intervals := len(w.items) - 1 - start
	if intervals <= 0 {
		return 0
	}

	first := w.items[start].timestamp
	last := w.items[len(w.items)-1].timestamp
	return last.Sub(first) / time.Duration(intervals)
}

// GetAvgBlockTime calculates the average time between blocks in the window.
// Returns the average duration in milliseconds, or 0 if not enough data.
func (w *RollingWindow) GetAvgBlockTime() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.items) < 2 {
		return 0
	}

	// Calculate average time between consecutive blocks
	first := w.items[0].timestamp
	last := w.items[len(w.items)-1].timestamp
	totalDuration := last.Sub(first)

	// Average = total duration / (number of intervals)
	intervals := len(w.items) - 1
	avgMs := float64(totalDuration.Milliseconds()) / float64(intervals)
	return avgMs
}

func (w *RollingWindow) Export() WindowSnapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()

	items := make([]ItemData, len(w.items))
	for i, it := range w.items {
		items[i] = ItemData{
			Timestamp:    it.timestamp,
			Participated: it.participated,
			Height:       it.height,
		}
	}

	return WindowSnapshot{
		Duration: w.duration,
		Items:    items,
		Missed:   w.missed,
	}
}

func (w *RollingWindow) Restore(snapshot WindowSnapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.duration = snapshot.Duration
	w.missed = snapshot.Missed
	w.items = make([]item, len(snapshot.Items))
	for i, data := range snapshot.Items {
		w.items[i] = item{
			timestamp:    data.Timestamp,
			participated: data.Participated,
			height:       data.Height,
		}
	}
}
