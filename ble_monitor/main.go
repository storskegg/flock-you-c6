package main

import (
	"bufio"
	"encoding/json"
	"flag"
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
	"go.bug.st/serial"
)

// Column width constants for TUI table
const (
	colWidthLastSeen = 21 // "YYYY-MM-DD hh:mm:ss" + padding
	colWidthMAC      = 20
	colWidthRSSI     = 8
	colWidthName     = 30
	colWidthMfrCode  = 10
	colPadding       = 5 // Total padding between columns
)

// Refresh rate for TUI
const refreshInterval = 250 * time.Millisecond

// RSSI threshold for near/far device separation
const nearDeviceRssiLimit = -75

// Special manufacturer code for bottom table display
const specialMfrCode = 76 // Apple Inc.

// Message represents both notification and BLE device messages
type Message struct {
	Notification *string  `json:"notification,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	MacAddress   string   `json:"mac_address,omitempty"`
	RSSI         int      `json:"rssi,omitempty"`
	MfrCode      int      `json:"mfr_code,omitempty"`
	DeviceName   string   `json:"device_name,omitempty"`
	ServiceUUIDs []string `json:"service_uuids,omitempty"`
}

// BLEDevice represents a Bluetooth LE device
type BLEDevice struct {
	MacAddress   string
	RSSI         int
	DeviceName   string
	MfrCode      int
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

	// Sort by LastSeen descending (newest first), then by MAC address ascending
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].LastSeen.Equal(devices[j].LastSeen) {
			return devices[i].MacAddress < devices[j].MacAddress
		}
		return devices[i].LastSeen.After(devices[j].LastSeen)
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
			LastSeen:     time.Now().UTC(),
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

// drawTable renders near devices, far devices, and special manufacturer tables to the screen
func drawTable(s tcell.Screen, devices []*BLEDevice, paused bool) {
	s.Clear()
	width, height := s.Size()

	// Calculate column widths using constants
	colWidths := []int{
		colWidthLastSeen,
		colWidthMAC,
		colWidthRSSI,
		colWidthName,
		colWidthMfrCode,
		width - colWidthLastSeen - colWidthMAC - colWidthRSSI - colWidthName - colWidthMfrCode - colPadding,
	}

	// Split devices into near, far, and special manufacturer
	var nearDevices, farDevices []*BLEDevice
	var specialMfrMACs []string

	for _, dev := range devices {
		if dev.MfrCode == specialMfrCode {
			specialMfrMACs = append(specialMfrMACs, dev.MacAddress)
		} else if dev.RSSI >= nearDeviceRssiLimit {
			nearDevices = append(nearDevices, dev)
		} else {
			farDevices = append(farDevices, dev)
		}
	}

	// Sort special manufacturer MAC addresses alphabetically
	sort.Strings(specialMfrMACs)

	// Draw status line at bottom
	statusStyle := tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite)
	statusText := "q: Quit | e: Export | p: Pause/Resume"
	if paused {
		statusText += " | [PAUSED]"
	}
	drawText(s, 0, height-1, width, statusStyle, statusText)

	// Calculate special table height (title + header + wrapped MAC address rows)
	mfrCodeColWidth := 15
	macListColWidth := width - mfrCodeColWidth
	macList := strings.Join(specialMfrMACs, ", ")
	wrappedMACLines := wordWrapMACs(macList, macListColWidth)
	specialTableHeight := 2 + len(wrappedMACLines) // title + header + data rows

	// Calculate available height for near/far tables (minus status line and special table)
	availableHeight := height - 1 - specialTableHeight

	// Split 50-50, with far devices getting -1 row if odd height
	nearTableHeight := availableHeight / 2
	if availableHeight%2 == 1 {
		nearTableHeight = (availableHeight / 2) + 1
	}

	// Draw near devices table
	row := 0
	row = drawDeviceTable(s, nearDevices, colWidths, "NEAR DEVICES", row, nearTableHeight)

	// Draw far devices table
	row = drawDeviceTable(s, farDevices, colWidths, "FAR DEVICES", row, availableHeight)

	// Draw special manufacturer table at the bottom (just above status line)
	drawSpecialMfrTable(s, specialMfrMACs, row, height-1)

	s.Show()
}

// drawSpecialMfrTable renders the special manufacturer code table
func drawSpecialMfrTable(s tcell.Screen, macAddresses []string, startRow int, maxRow int) {
	width, _ := s.Size()

	// Draw table title
	titleStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorDarkGreen).Foreground(tcell.ColorWhite)
	drawText(s, 0, startRow, width, titleStyle, fmt.Sprintf(" MFR CODE %d ", specialMfrCode))
	startRow++

	// Draw header
	headerStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorNavy).Foreground(tcell.ColorWhite)
	mfrCodeColWidth := 15
	macListColWidth := width - mfrCodeColWidth

	drawText(s, 0, startRow, mfrCodeColWidth, headerStyle, "Mfr Code")
	drawText(s, mfrCodeColWidth, startRow, macListColWidth, headerStyle, "MAC Addresses")
	startRow++

	// Draw data rows with word-wrapped MAC addresses
	normalStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)

	// Draw Mfr Code in first row only
	drawText(s, 0, startRow, mfrCodeColWidth, normalStyle, fmt.Sprintf("%d", specialMfrCode))

	// Word-wrap MAC addresses at comma/whitespace boundaries
	macList := strings.Join(macAddresses, ", ")
	wrappedLines := wordWrapMACs(macList, macListColWidth)

	for i, line := range wrappedLines {
		if startRow+i >= maxRow {
			break
		}
		drawText(s, mfrCodeColWidth, startRow+i, macListColWidth, normalStyle, line)
	}
}

// wordWrapMACs wraps a comma-separated list of MAC addresses to fit within maxWidth
// Only breaks at comma+space boundaries, never in the middle of a MAC address
func wordWrapMACs(text string, maxWidth int) []string {
	if len(text) <= maxWidth {
		return []string{text}
	}

	var lines []string
	currentLine := ""

	// Split by ", " to get individual MAC addresses
	macs := strings.Split(text, ", ")

	for _, mac := range macs {
		// Check if adding this MAC (with separator if needed) would exceed width
		testLine := currentLine
		if currentLine != "" {
			testLine += ", "
		}
		testLine += mac

		if len(testLine) <= maxWidth {
			// Fits on current line
			currentLine = testLine
		} else {
			// Doesn't fit, start new line
			if currentLine != "" {
				lines = append(lines, currentLine)
			}
			currentLine = mac
		}
	}

	// Add the last line
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return lines
}

// drawDeviceTable renders a single device table with the given title
func drawDeviceTable(s tcell.Screen, devices []*BLEDevice, colWidths []int, title string, startRow int, maxRow int) int {
	width, _ := s.Size()

	// Draw table title
	titleStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorDarkGreen).Foreground(tcell.ColorWhite)
	drawText(s, 0, startRow, width, titleStyle, fmt.Sprintf(" %s ", title))
	startRow++

	// Draw header
	headerStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorNavy).Foreground(tcell.ColorWhite)
	headers := []string{"Last Seen", "MAC Address", "RSSI", "Device Name", "Mfr Code", "Service UUIDs"}

	col := 0
	for i, header := range headers {
		drawText(s, col, startRow, colWidths[i], headerStyle, header)
		col += colWidths[i]
	}
	startRow++

	// Draw devices
	row := startRow
	normalStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)

	for _, dev := range devices {
		if row >= maxRow {
			break
		}

		// Calculate number of lines needed for service UUIDs
		uuidLines := 1
		if len(dev.ServiceUUIDs) > 1 {
			uuidLines = len(dev.ServiceUUIDs)
		}

		// Draw Last Seen timestamp (first column)
		lastSeenStr := dev.LastSeen.Format("2006-01-02 15:04:05")
		drawText(s, 0, row, colWidths[0], normalStyle, lastSeenStr)

		// Draw MAC address
		drawText(s, colWidths[0], row, colWidths[1], normalStyle, dev.MacAddress)

		// Draw RSSI
		drawText(s, colWidths[0]+colWidths[1], row, colWidths[2], normalStyle, fmt.Sprintf("%d", dev.RSSI))

		// Draw device name
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2], row, colWidths[3], normalStyle, dev.DeviceName)

		// Draw Mfr Code (as integer)
		mfrCodeStr := ""
		if dev.MfrCode != 0 {
			mfrCodeStr = fmt.Sprintf("%d", dev.MfrCode)
		}
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2]+colWidths[3], row, colWidths[4], normalStyle, mfrCodeStr)

		// Draw service UUIDs (multi-line with ellipsis support)
		uuidCol := colWidths[0] + colWidths[1] + colWidths[2] + colWidths[3] + colWidths[4]
		if len(dev.ServiceUUIDs) == 0 {
			drawText(s, uuidCol, row, colWidths[5], normalStyle, "")
		} else {
			for i, uuid := range dev.ServiceUUIDs {
				if row+i >= maxRow {
					break
				}
				// Ellipsize if UUID is longer than column width
				displayUUID := uuid
				if len(uuid) > colWidths[5] && colWidths[5] > 3 {
					displayUUID = uuid[:colWidths[5]-3] + "..."
				}
				drawText(s, uuidCol, row+i, colWidths[5], normalStyle, displayUUID)
			}
		}

		row += uuidLines
	}

	return row
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
	// Command-line flags
	serialPort := flag.String("port", "", "Serial port device (e.g., /dev/ttyUSB0). If not specified, reads from stdin.")
	baudRate := flag.Int("baud", 115200, "Baud rate for serial port (default: 115200)")
	flag.Parse()

	// Initialize aggregator
	agg := NewAggregator()

	// Paused state
	var paused bool
	var pauseMu sync.RWMutex

	// Done channel for graceful shutdown
	done := make(chan struct{})

	// Determine input source
	var reader io.ReadCloser
	if *serialPort != "" {
		// Open serial port
		mode := &serial.Mode{
			BaudRate: *baudRate,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		}
		port, err := serial.Open(*serialPort, mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening serial port %s: %v\n", *serialPort, err)
			os.Exit(1)
		}
		reader = port
		defer port.Close()
	} else {
		// Use stdin
		reader = os.Stdin
	}

	// Start reading from input source
	go readSerial(reader, agg, &paused, &pauseMu, done)

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
	ticker := time.NewTicker(refreshInterval)
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
