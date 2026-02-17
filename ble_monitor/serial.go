package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"go.bug.st/serial"
)

// parseISO8601 parses an ISO 8601 timestamp from the firmware (e.g. "2026-02-16T18:08:32Z")
func parseISO8601(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}

// ConnectionState tracks serial connection status
type ConnectionState struct {
	mu            sync.RWMutex
	connected     bool
	lastErrorTime time.Time
	totalAttempts int
	modalShown    bool // Track if disconnection modal is currently displayed
}

func (cs *ConnectionState) SetConnected(connected bool) {
	cs.mu.Lock()
	cs.connected = connected
	if connected {
		cs.totalAttempts = 0
	}
	cs.mu.Unlock()
}

func (cs *ConnectionState) SetError(err error) {
	cs.mu.Lock()
	cs.lastErrorTime = time.Now()
	cs.totalAttempts++
	cs.mu.Unlock()
}

func (cs *ConnectionState) GetStatus() (bool, time.Time, int) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.connected, cs.lastErrorTime, cs.totalAttempts
}

func (cs *ConnectionState) SetModalShown(shown bool) {
	cs.mu.Lock()
	cs.modalShown = shown
	cs.mu.Unlock()
}

func (cs *ConnectionState) IsModalShown() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.modalShown
}

// openSerialPort attempts to open a serial port with the given configuration
func openSerialPort(portPath string, baudRate int) (io.ReadCloser, error) {
	mode := &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	return serial.Open(portPath, mode)
}

// readSerial reads from reader and processes lines, with automatic reconnection for serial ports
// Reconnection attempts continue indefinitely with exponential backoff until success or app quit
func readSerial(portPath string, baudRate int, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex, connState *ConnectionState, done <-chan struct{}) {
	var reader io.ReadCloser
	var err error

	// If portPath is empty, try reading from stdin (only if piped, not a terminal)
	if portPath == "" {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			// stdin is a terminal — don't read from it (conflicts with tcell)
			connState.SetConnected(false)
			<-done
			return
		}
		reader = os.Stdin
		connState.SetConnected(true)
		readSerialLoop(reader, agg, paused, pauseMu, connState, done)
		return
	}

	// For serial ports, implement reconnection logic
	reconnectDelay := 1 * time.Second
	maxReconnectDelay := 5 * time.Second

	for {
		select {
		case <-done:
			if reader != nil {
				reader.Close()
			}
			return
		default:
		}

		// Attempt to open/reopen the serial port
		reader, err = openSerialPort(portPath, baudRate)
		if err != nil {
			wasConnected := false
			connState.mu.RLock()
			wasConnected = connState.connected
			connState.mu.RUnlock()

			connState.SetConnected(false)
			connState.SetError(err)

			// Play disconnect sound only on first failure (not repeated attempts)
			if wasConnected {
				playDisconnectSound()
			} else {
				// Play reconnect attempt sound for subsequent failures
				playReconnectAttemptSound()
			}

			// Wait before retrying
			select {
			case <-done:
				return
			case <-time.After(reconnectDelay):
				// Exponential backoff, max 30 seconds
				reconnectDelay += 1
				if reconnectDelay > maxReconnectDelay {
					reconnectDelay = maxReconnectDelay
				}
			}
			continue
		}

		// Successfully connected
		connState.SetConnected(true)
		reconnectDelay = 1 * time.Second // Reset backoff

		// Play success sound
		playConnectedSound()

		// Read from the port until error or done
		err = readSerialLoop(reader, agg, paused, pauseMu, connState, done)

		// Close the port
		reader.Close()

		// If we're done, exit
		select {
		case <-done:
			return
		default:
		}

		// Connection lost, mark as disconnected and retry
		connState.SetConnected(false)
		if err != nil {
			connState.SetError(err)
		}

		// Brief delay before reconnect attempt
		select {
		case <-done:
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// readSerialLoop performs the actual reading and processing
func readSerialLoop(reader io.ReadCloser, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex, connState *ConnectionState, done <-chan struct{}) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // Increase buffer for large lines

	for {
		select {
		case <-done:
			return nil
		default:
			if scanner.Scan() {
				// Use Bytes() instead of Text() to avoid allocation
				line := scanner.Bytes()
				// Process immediately in this goroutine for minimal latency
				processSerialLine(line, agg, paused, pauseMu)
			} else {
				if err := scanner.Err(); err != nil {
					// Scanner error (likely connection issue)
					return err
				}
				// EOF - connection closed
				return io.EOF
			}
		}
	}
}

// processSerialLine processes a single line of JSON
func processSerialLine(line []byte, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex) {
	// Check if paused
	pauseMu.RLock()
	isPaused := *paused
	pauseMu.RUnlock()

	if isPaused {
		return // Discard when paused
	}

	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return // Silently ignore malformed JSON
	}

	// Handle notification
	if msg.Notification != nil {
		// Just beep
		fmt.Print("\a")
		return
	}

	// Handle BLE device
	if msg.MacAddress != "" {
		device := &BLEDevice{
			MacAddress:   msg.MacAddress,
			RSSI:         msg.RSSI,
			DeviceName:   msg.DeviceName,
			MfrCode:      msg.MfrCode,
			MfrData:      msg.MfrData,
			ServiceUUIDs: msg.ServiceUUIDs,
			LastSeen:     time.Now().UTC(),
			GeoData:      NewRSSILocationMap(),
		}

		// Add or update the device in the aggregator
		agg.AddOrUpdate(device)

		// Push firmware GNSS location to the stored device (only if spatial fix)
		if msg.Fix >= 2 && msg.Fix <= 3 {
			loc := GeoLocation{
				Latitude:  msg.Lat,
				Longitude: msg.Lon,
				Accuracy:  float64(msg.HAcc) / 1000.0, // mm to meters
				Timestamp: parseISO8601(msg.Timestamp),
			}
			agg.mu.Lock()
			if storedDev, exists := agg.devices[msg.MacAddress]; exists && storedDev.GeoData != nil {
				storedDev.GeoData.Push(msg.RSSI, loc)
			}
			agg.mu.Unlock()
		}
	}
}
