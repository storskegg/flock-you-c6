package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
)

// Message represents both notification and BLE device messages
type Message struct {
	Notification *string  `json:"notification,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	MacAddress   string   `json:"mac_address,omitempty"`
	RSSI         int      `json:"rssi,omitempty"`
	MfrCode      string   `json:"mfr_code,omitempty"`
	DeviceName   string   `json:"device_name,omitempty"`
	ServiceUUIDs []string `json:"service_uuids,omitempty"`
}

// BLEDevice represents a Bluetooth LE device
type BLEDevice struct {
	MacAddress   string
	RSSI         int
	DeviceName   string
	MfrCode      string
	ServiceUUIDs []string
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
	a.devices[device.MacAddress] = device
	a.mu.Unlock()
}

func (a *Aggregator) GetSorted() []*BLEDevice {
	a.mu.RLock()
	defer a.mu.RUnlock()

	devices := make([]*BLEDevice, 0, len(a.devices))
	for _, dev := range a.devices {
		devices = append(devices, dev)
	}

	// Sort by MAC address
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].MacAddress < devices[j].MacAddress
	})

	return devices
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

// processSerialLine processes a single line of JSON
func processSerialLine(line string, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex) {
	// Check if paused
	pauseMu.RLock()
	isPaused := *paused
	pauseMu.RUnlock()

	if isPaused {
		return // Discard when paused
	}

	var msg Message
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
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
			ServiceUUIDs: msg.ServiceUUIDs,
		}
		agg.AddOrUpdate(device)
	}
}

// readSerial reads from reader and processes lines non-blocking
func readSerial(reader io.Reader, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex, done <-chan struct{}) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // Increase buffer for large lines

	for {
		select {
		case <-done:
			return
		default:
			if scanner.Scan() {
				line := scanner.Text()
				// Process immediately in this goroutine for minimal latency
				processSerialLine(line, agg, paused, pauseMu)
			} else {
				if err := scanner.Err(); err != nil {
					return
				}
				// EOF or no data available
				time.Sleep(1 * time.Millisecond)
			}
		}
	}
}

// drawTable renders the table to the screen
func drawTable(s tcell.Screen, devices []*BLEDevice, paused bool) {
	s.Clear()
	width, height := s.Size()

	// Draw header
	headerStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorNavy).Foreground(tcell.ColorWhite)
	headers := []string{"MAC Address", "RSSI", "Device Name", "Mfr Code", "Service UUIDs"}
	colWidths := []int{17, 6, 20, 10, width - 17 - 6 - 20 - 10 - 4}

	col := 0
	for i, header := range headers {
		drawText(s, col, 0, colWidths[i], headerStyle, header)
		col += colWidths[i]
	}

	// Draw status line
	statusStyle := tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite)
	statusText := "q: Quit | e: Export | p: Pause/Resume"
	if paused {
		statusText += " | [PAUSED]"
	}
	drawText(s, 0, height-1, width, statusStyle, statusText)

	// Draw devices
	row := 1
	normalStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)

	for _, dev := range devices {
		if row >= height-1 {
			break
		}

		// Calculate number of lines needed for service UUIDs
		uuidLines := 1
		if len(dev.ServiceUUIDs) > 1 {
			uuidLines = len(dev.ServiceUUIDs)
		}

		// Draw MAC address
		drawText(s, 0, row, colWidths[0], normalStyle, dev.MacAddress)

		// Draw RSSI
		drawText(s, colWidths[0], row, colWidths[1], normalStyle, fmt.Sprintf("%d", dev.RSSI))

		// Draw device name
		drawText(s, colWidths[0]+colWidths[1], row, colWidths[2], normalStyle, dev.DeviceName)

		// Draw Mfr Code
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2], row, colWidths[3], normalStyle, dev.MfrCode)

		// Draw service UUIDs (multi-line)
		uuidCol := colWidths[0] + colWidths[1] + colWidths[2] + colWidths[3]
		if len(dev.ServiceUUIDs) == 0 {
			drawText(s, uuidCol, row, colWidths[4], normalStyle, "")
		} else {
			for i, uuid := range dev.ServiceUUIDs {
				if row+i >= height-1 {
					break
				}
				drawText(s, uuidCol, row+i, colWidths[4], normalStyle, uuid)
			}
		}

		row += uuidLines
	}

	s.Show()
}

// drawText draws text at a specific position
func drawText(s tcell.Screen, x, y, width int, style tcell.Style, text string) {
	for i := 0; i < width && i < len(text); i++ {
		s.SetContent(x+i, y, rune(text[i]), nil, style)
	}
	// Fill remaining space
	for i := len(text); i < width; i++ {
		s.SetContent(x+i, y, ' ', nil, style)
	}
}

func main() {
	// Initialize aggregator
	agg := NewAggregator()

	// Paused state
	var paused bool
	var pauseMu sync.RWMutex

	// Done channel for graceful shutdown
	done := make(chan struct{})

	// Start reading from stdin (serial input)
	go readSerial(os.Stdin, agg, &paused, &pauseMu, done)

	// Initialize screen
	s, err := tcell.NewScreen()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating screen: %v\n", err)
		os.Exit(1)
	}
	if err := s.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing screen: %v\n", err)
		os.Exit(1)
	}
	defer s.Fini()

	s.SetStyle(tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite))

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Ticker for refresh
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Initial draw
	drawTable(s, agg.GetSorted(), paused)

	// Event loop
	quit := false
	for !quit {
		select {
		case <-ticker.C:
			pauseMu.RLock()
			isPaused := paused
			pauseMu.RUnlock()
			drawTable(s, agg.GetSorted(), isPaused)

		case <-sigChan:
			quit = true

		default:
			// Check for key events (non-blocking)
			if s.HasPendingEvent() {
				ev := s.PollEvent()
				switch ev := ev.(type) {
				case *tcell.EventKey:
					switch ev.Key() {
					case tcell.KeyRune:
						switch ev.Rune() {
						case 'q', 'Q':
							quit = true
						case 'e', 'E':
							if err := agg.ExportJSON("ble_devices.json"); err != nil {
								// Could show error in status line, but for now ignore
							}
						case 'p', 'P':
							pauseMu.Lock()
							paused = !paused
							pauseMu.Unlock()
						}
					case tcell.KeyCtrlC:
						quit = true
					}
				case *tcell.EventResize:
					s.Sync()
					pauseMu.RLock()
					isPaused := paused
					pauseMu.RUnlock()
					drawTable(s, agg.GetSorted(), isPaused)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(done)
}
