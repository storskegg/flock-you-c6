package main

import (
	"sync"
	"time"
)

// GeoLocation represents a geographic position with accuracy and timestamp
type GeoLocation struct {
	Latitude  float64
	Longitude float64
	Elevation float64
	Accuracy  float64 // HDOP or similar quality metric
	Timestamp time.Time
}

// RingBuffer is a generic FIFO buffer with fixed capacity
type RingBuffer[T any] struct {
	data     []T
	capacity int
	head     int
	size     int
}

// NewRingBuffer creates a new ring buffer with the given capacity
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
		head:     0,
		size:     0,
	}
}

// Push adds an item to the ring buffer, removing the oldest if at capacity
func (rb *RingBuffer[T]) Push(item T) {
	rb.data[rb.head] = item
	rb.head = (rb.head + 1) % rb.capacity
	if rb.size < rb.capacity {
		rb.size++
	}
}

// GetAll returns all items in the ring buffer (oldest to newest)
func (rb *RingBuffer[T]) GetAll() []T {
	if rb.size == 0 {
		return []T{}
	}

	result := make([]T, rb.size)
	if rb.size < rb.capacity {
		// Buffer not full yet, items are from 0 to size-1
		copy(result, rb.data[:rb.size])
	} else {
		// Buffer is full, need to handle wraparound
		// Items from head to end are oldest
		// Items from 0 to head-1 are newest
		tailSize := rb.capacity - rb.head
		copy(result, rb.data[rb.head:])
		copy(result[tailSize:], rb.data[:rb.head])
	}
	return result
}

// Size returns the current number of items in the buffer
func (rb *RingBuffer[T]) Size() int {
	return rb.size
}

// RSSILocationMap maintains geo locations for the top 3 observed RSSI values
// Each RSSI gets a ring buffer of recent locations
type RSSILocationMap struct {
	mu       sync.RWMutex
	data     map[int]*RingBuffer[GeoLocation]
	topRSSIs []int // Sorted descending (highest first), max 3 entries
}

// NewRSSILocationMap creates a new RSSI location map
func NewRSSILocationMap() *RSSILocationMap {
	return &RSSILocationMap{
		data:     make(map[int]*RingBuffer[GeoLocation]),
		topRSSIs: make([]int, 0, 3),
	}
}

// Push adds a location for the given RSSI
// Only the top 3 RSSIs are kept; lower RSSIs are ignored
func (rlm *RSSILocationMap) Push(rssi int, loc GeoLocation) {
	rlm.mu.Lock()
	defer rlm.mu.Unlock()

	// Check if this RSSI is in the top 3
	isTop3 := false
	position := -1

	for i, topRSSI := range rlm.topRSSIs {
		if rssi == topRSSI {
			// Already in top 3
			isTop3 = true
			position = i
			break
		} else if rssi > topRSSI {
			// New RSSI is higher than this one
			isTop3 = true
			position = i
			break
		}
	}

	// If not in top 3 and we already have 3, check if it's higher than the lowest
	if !isTop3 && len(rlm.topRSSIs) < 3 {
		isTop3 = true
		position = len(rlm.topRSSIs)
	}

	// If not worthy of top 3, discard
	if !isTop3 {
		return
	}

	// If RSSI already exists in top 3 (found in loop), just push to its buffer
	if position >= 0 && position < len(rlm.topRSSIs) && rssi == rlm.topRSSIs[position] {
		// RSSI already exists, buffer should exist too
		if rlm.data[rssi] != nil {
			rlm.data[rssi].Push(loc)
		}
		return
	}

	// New RSSI needs to be inserted at position
	// Create buffer if it doesn't exist
	if _, exists := rlm.data[rssi]; !exists {
		rlm.data[rssi] = NewRingBuffer[GeoLocation](3) // Capacity of 3 per RSSI
	}
	rlm.data[rssi].Push(loc)

	// Update topRSSIs list
	if len(rlm.topRSSIs) < 3 {
		// Less than 3 entries, insert at position
		rlm.topRSSIs = append(rlm.topRSSIs, 0)
		copy(rlm.topRSSIs[position+1:], rlm.topRSSIs[position:])
		rlm.topRSSIs[position] = rssi
	} else {
		// Already have 3, need to evict the lowest if this is higher
		if position < 3 {
			// Remove the lowest RSSI's data
			lowestRSSI := rlm.topRSSIs[2]
			delete(rlm.data, lowestRSSI)

			// Shift and insert
			copy(rlm.topRSSIs[position+1:], rlm.topRSSIs[position:2])
			rlm.topRSSIs[position] = rssi
		}
	}
}

// GetLocation returns the mean location of all entries in the highest RSSI's buffer
// If the highest RSSI has no data, falls back to the next available RSSI
// Returns nil if no location data exists at all
func (rlm *RSSILocationMap) GetLocation() *GeoLocation {
	rlm.mu.RLock()
	defer rlm.mu.RUnlock()

	if len(rlm.topRSSIs) == 0 {
		return nil
	}

	// Try each RSSI in order (highest to lowest) until we find one with data
	for _, rssi := range rlm.topRSSIs {
		buffer := rlm.data[rssi]

		if buffer == nil || buffer.Size() == 0 {
			continue // Try next RSSI
		}

		// Get all locations from the buffer
		locations := buffer.GetAll()
		if len(locations) == 0 {
			continue // Try next RSSI
		}

		// Calculate means
		var sumLat, sumLon, sumEl, sumAcc float64
		for _, loc := range locations {
			sumLat += loc.Latitude
			sumLon += loc.Longitude
			sumEl += loc.Elevation
			sumAcc += loc.Accuracy
		}

		count := float64(len(locations))
		return &GeoLocation{
			Latitude:  sumLat / count,
			Longitude: sumLon / count,
			Elevation: sumEl / count,
			Accuracy:  sumAcc / count,
			// Timestamp is omitted (not averaged)
		}
	}

	// No RSSI has any location data
	return nil
}

// LocationState manages the current GPS/GNSS location in a thread-safe manner
type LocationState struct {
	mu                    sync.RWMutex
	current               *GeoLocation
	lastUpdate            time.Time
	fixQuality            int    // 0 = no fix, 1 = GPS fix, 2 = DGPS fix, etc.
	satellites            int    // Number of satellites in use
	satellitesInView      int    // Number of satellites in view (total across all constellations)
	status                string // "detecting", "failed", "no_fix", "fix"
	gpsFailureDismissed   bool   // Whether the GPS failure modal has been dismissed
	gpsConnected          bool   // Whether GPS port is currently connected
	gpsReconnecting       bool   // Whether GPS is currently attempting reconnection
	gpsReconnectDismissed bool   // Whether the GPS reconnection modal has been dismissed
	gpsLastDisconnectTime time.Time
	gpsReconnectAttempts  int
}

// NewLocationState creates a new location state manager
func NewLocationState() *LocationState {
	return &LocationState{
		status: "no_gps", // Default: no GPS device configured
	}
}

// SetCurrent updates the current location
func (ls *LocationState) SetCurrent(loc *GeoLocation, fixQuality int, satellites int, satellitesInView int) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	ls.current = loc
	ls.lastUpdate = time.Now()
	ls.fixQuality = fixQuality
	ls.satellites = satellites
	ls.satellitesInView = satellitesInView

	if fixQuality > 0 {
		ls.status = "fix"
	} else {
		ls.status = "no_fix"
	}
}

// GetCurrent returns the current location (or nil if none)
func (ls *LocationState) GetCurrent() *GeoLocation {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.current
}

// GetStatus returns the current GPS status and details
func (ls *LocationState) GetStatus() (status string, fixQuality int, satellites int, satellitesInView int, lastUpdate time.Time) {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.status, ls.fixQuality, ls.satellites, ls.satellitesInView, ls.lastUpdate
}

// SetStatus updates the GPS status (detecting, failed, no_fix, fix)
func (ls *LocationState) SetStatus(status string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.status = status
}

// DismissGPSFailure marks the GPS failure modal as dismissed
func (ls *LocationState) DismissGPSFailure() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.gpsFailureDismissed = true
}

// ShouldShowGPSFailureModal returns true if the modal should be shown
func (ls *LocationState) ShouldShowGPSFailureModal() bool {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.status == "failed" && !ls.gpsFailureDismissed
}

// SetGPSConnected updates the GPS connection state
func (ls *LocationState) SetGPSConnected(connected bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	wasConnected := ls.gpsConnected
	ls.gpsConnected = connected

	if !connected && wasConnected {
		// Just disconnected
		ls.gpsReconnecting = true
		ls.gpsLastDisconnectTime = time.Now()
		ls.gpsReconnectAttempts = 0
		ls.gpsReconnectDismissed = false
	} else if connected && !wasConnected {
		// Just reconnected
		ls.gpsReconnecting = false
		ls.gpsReconnectAttempts = 0
	}
}

// SetGPSReconnectAttempt increments the reconnection attempt counter
func (ls *LocationState) SetGPSReconnectAttempt() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.gpsReconnectAttempts++
}

// DismissGPSReconnect marks the GPS reconnection modal as dismissed
func (ls *LocationState) DismissGPSReconnect() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.gpsReconnectDismissed = true
}

// ShouldShowGPSReconnectModal returns true if the reconnection modal should be shown
func (ls *LocationState) ShouldShowGPSReconnectModal() bool {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.gpsReconnecting && !ls.gpsReconnectDismissed
}

// GetGPSReconnectInfo returns reconnection details
func (ls *LocationState) GetGPSReconnectInfo() (attempts int, elapsed time.Duration) {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.gpsReconnectAttempts, time.Since(ls.gpsLastDisconnectTime)
}
