package main

import (
	"os"
	"sort"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

// Time threshold for recent/stale device separation
const recentDeviceThreshold = 10 * time.Second

// SortedDevices holds recently seen and stale devices separately
type SortedDevices struct {
	Recent []*BLEDevice
	Stale  []*BLEDevice
}

// Message represents both notification and BLE device messages
type Message struct {
	Notification *string  `json:"notification,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	MacAddress   string   `json:"mac_address,omitempty"`
	RSSI         int      `json:"rssi,omitempty"`
	MfrCode      int      `json:"mfr_code,omitempty"`
	MfrData      string   `json:"mfr_data,omitempty"`
	DeviceName   string   `json:"device_name,omitempty"`
	ServiceUUIDs []string `json:"service_uuids,omitempty"`
}

// BLEDevice represents a Bluetooth LE device
type BLEDevice struct {
	MacAddress   string
	RSSI         int
	DeviceName   string
	MfrCode      int
	MfrData      string
	ServiceUUIDs []string
	LastSeen     time.Time
	Count        int              // Number of times device has been observed
	GeoData      *RSSILocationMap // Geographic data keyed by all RSSIs
}

// Aggregator stores BLE devices indexed by MAC address
type Aggregator struct {
	mu      sync.RWMutex
	devices map[string]*BLEDevice
}

func NewAggregator() *Aggregator {
	return &Aggregator{
		devices: make(map[string]*BLEDevice),
	}
}

func (a *Aggregator) AddOrUpdate(device *BLEDevice) {
	a.mu.Lock()
	defer a.mu.Unlock()

	existing, exists := a.devices[device.MacAddress]
	if !exists {
		// New device, initialize count to 1
		device.Count = 1
		a.devices[device.MacAddress] = device
		return
	}

	// Device exists - increment observation count
	existing.Count++

	// Apply update rules for each field:
	// - If existing field is empty, update it
	// - If existing field is not empty and new field is not empty, update it
	// - If existing field is not empty and new field is empty, keep existing

	// Update RSSI (always update, it's an int)
	existing.RSSI = device.RSSI

	// Update LastSeen (always update)
	existing.LastSeen = device.LastSeen

	// Update DeviceName
	if existing.DeviceName == "" || device.DeviceName != "" {
		existing.DeviceName = device.DeviceName
	}

	// Update MfrCode (always update if non-zero)
	if existing.MfrCode == 0 || device.MfrCode != 0 {
		existing.MfrCode = device.MfrCode
	}

	// Update MfrData
	if existing.MfrData == "" || device.MfrData != "" {
		existing.MfrData = device.MfrData
	}

	// Update ServiceUUIDs
	if len(existing.ServiceUUIDs) == 0 || len(device.ServiceUUIDs) > 0 {
		existing.ServiceUUIDs = device.ServiceUUIDs
	}

	// Ensure GeoData exists (initialize if needed)
	if existing.GeoData == nil {
		existing.GeoData = NewRSSILocationMap()
	}
}

func (a *Aggregator) GetSorted() *SortedDevices {
	a.mu.RLock()
	defer a.mu.RUnlock()

	totalDevices := len(a.devices)
	devices := make([]*BLEDevice, 0, totalDevices)
	for _, dev := range a.devices {
		devices = append(devices, dev)
	}

	now := time.Now().UTC()

	// Pre-allocate with capacity hints (estimate 50/50 split)
	recentDevices := make([]*BLEDevice, 0, totalDevices/2)
	staleDevices := make([]*BLEDevice, 0, totalDevices/2)

	// Separate devices by last seen time
	for _, dev := range devices {
		if now.Sub(dev.LastSeen) <= recentDeviceThreshold {
			recentDevices = append(recentDevices, dev)
		} else {
			staleDevices = append(staleDevices, dev)
		}
	}

	// Sort recent devices alphabetically by MAC address
	sort.Slice(recentDevices, func(i, j int) bool {
		return recentDevices[i].MacAddress < recentDevices[j].MacAddress
	})

	// Pre-compute truncated times for stale devices to avoid repeated Truncate() calls
	type cachedTime struct {
		dev       *BLEDevice
		truncTime time.Time
	}
	cached := make([]cachedTime, len(staleDevices))
	for i, dev := range staleDevices {
		cached[i] = cachedTime{
			dev:       dev,
			truncTime: dev.LastSeen.Truncate(time.Second),
		}
	}

	// Sort stale devices by truncated LastSeen descending, then by MAC address
	sort.Slice(cached, func(i, j int) bool {
		if cached[i].truncTime.Equal(cached[j].truncTime) {
			return cached[i].dev.MacAddress < cached[j].dev.MacAddress
		}
		return cached[i].truncTime.After(cached[j].truncTime)
	})

	// Extract sorted devices back to staleDevices slice
	for i, c := range cached {
		staleDevices[i] = c.dev
	}

	return &SortedDevices{
		Recent: recentDevices,
		Stale:  staleDevices,
	}
}

func (a *Aggregator) ExportJSON(filename string) error {
	sorted := a.GetSorted()

	// Combine for export (recent first, then stale)
	allDevices := make([]*BLEDevice, 0, len(sorted.Recent)+len(sorted.Stale))
	allDevices = append(allDevices, sorted.Recent...)
	allDevices = append(allDevices, sorted.Stale...)

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(allDevices)
}

func (a *Aggregator) Clear() {
	a.mu.Lock()
	a.devices = make(map[string]*BLEDevice)
	a.mu.Unlock()
}
