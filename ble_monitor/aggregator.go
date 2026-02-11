package main

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Time threshold for recent/stale device separation
const recentDeviceThreshold = 10 * time.Second

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
		// New device, just add it
		a.devices[device.MacAddress] = device
		return
	}

	// Device exists, apply update rules for each field:
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
}

func (a *Aggregator) GetSorted() []*BLEDevice {
	a.mu.RLock()
	defer a.mu.RUnlock()

	devices := make([]*BLEDevice, 0, len(a.devices))
	for _, dev := range a.devices {
		devices = append(devices, dev)
	}

	now := time.Now().UTC()
	var recentDevices, staleDevices []*BLEDevice

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

	// Sort stale devices by LastSeen descending (newest first), then by MAC address
	sort.Slice(staleDevices, func(i, j int) bool {
		// Truncate to 1-second precision for comparison
		iTime := staleDevices[i].LastSeen.Truncate(time.Second)
		jTime := staleDevices[j].LastSeen.Truncate(time.Second)

		if iTime.Equal(jTime) {
			return staleDevices[i].MacAddress < staleDevices[j].MacAddress
		}
		return iTime.After(jTime)
	})

	// Combine: recent devices first, then stale devices
	result := make([]*BLEDevice, 0, len(devices))
	result = append(result, recentDevices...)
	result = append(result, staleDevices...)

	return result
}

func (a *Aggregator) ExportJSON(filename string) error {
	devices := a.GetSorted()

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(devices)
}

func (a *Aggregator) Clear() {
	a.mu.Lock()
	a.devices = make(map[string]*BLEDevice)
	a.mu.Unlock()
}
