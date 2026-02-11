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
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/gen2brain/beeep"
	"go.bug.st/serial"
)

// Column width constants for TUI table
const (
	colWidthLastSeen     = 21 // "YYYY-MM-DD hh:mm:ss" + padding
	colWidthMAC          = 19
	colWidthSignal       = 9 // Signal strength indicator
	colWidthRSSI         = 6
	colWidthName         = 30
	colWidthServiceUUIDs = 38 // Fixed width, moved between Name and MfrCode
	colWidthMfrCode      = 8
)

// Time threshold for recent/stale device separation
const recentDeviceThreshold = 10 * time.Second

// TableState tracks scrolling and focus state for the tables
type TableState struct {
	nearScrollOffset int
	farScrollOffset  int
	focusedTable     string // "near" or "far"
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

// Sound notification functions - all run in goroutines to avoid blocking

func playDisconnectSound() {
	go func() {
		// Low frequency, longer duration - ominous
		beeep.Beep(400, 300)
	}()
}

func playReconnectAttemptSound() {
	go func() {
		// Mid frequency, short blip
		beeep.Beep(600, 100)
	}()
}

func playConnectedSound() {
	go func() {
		// Ascending two-tone success melody
		beeep.Beep(600, 150)
		time.Sleep(50 * time.Millisecond)
		beeep.Beep(800, 150)
	}()
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
			MfrData:      msg.MfrData,
			ServiceUUIDs: msg.ServiceUUIDs,
			LastSeen:     time.Now().UTC(),
		}
		agg.AddOrUpdate(device)
	}
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

	// If portPath is empty, we're reading from stdin (no reconnection)
	if portPath == "" {
		reader = os.Stdin
		connState.SetConnected(true)
		readSerialLoop(reader, agg, paused, pauseMu, connState, done)
		return
	}

	// For serial ports, implement reconnection logic
	reconnectDelay := 1 * time.Second
	maxReconnectDelay := 30 * time.Second

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
				reconnectDelay *= 2
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
				line := scanner.Text()
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

// drawTable renders near devices, far devices, and special manufacturer tables to the screen
func drawTable(s tcell.Screen, devices []*BLEDevice, paused bool, state *TableState, connState *ConnectionState) {
	s.Clear()
	width, height := s.Size()

	// Calculate column widths using constants
	// Order: Last Seen, MAC, Signal, RSSI, Name, Service UUIDs, Mfr ID, Mfr Data (variable)
	colWidths := []int{
		colWidthLastSeen,
		colWidthMAC,
		colWidthSignal,
		colWidthRSSI,
		colWidthName,
		colWidthServiceUUIDs,
		colWidthMfrCode,
		width - colWidthLastSeen - colWidthMAC - colWidthSignal - colWidthRSSI - colWidthName - colWidthServiceUUIDs - colWidthMfrCode,
	}

	// Split devices into recent and stale based on last seen time
	now := time.Now().UTC()
	var recentDevices, staleDevices []*BLEDevice

	for _, dev := range devices {
		if now.Sub(dev.LastSeen) <= recentDeviceThreshold {
			recentDevices = append(recentDevices, dev)
		} else {
			staleDevices = append(staleDevices, dev)
		}
	}

	// Calculate available height for near/far tables (minus status line)
	availableHeight := height - 1

	// Split 50-50, with far devices getting -1 row if odd height
	nearTableHeight := availableHeight / 2
	if availableHeight%2 == 1 {
		nearTableHeight = (availableHeight / 2) + 1
	}

	// Draw status line at bottom
	statusStyle := tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite)
	statusText := "q: Quit | e: Export | c: Clear | p: Pause | ↑↓/jk: Scroll | Tab: Switch | PgUp/PgDn/Home/End"
	if paused {
		statusText += " | [PAUSED]"
	}

	// Add connection status
	connected, lastErrTime, attempts := connState.GetStatus()
	if connected {
		statusText += " | ✓ CONNECTED"
	} else {
		if attempts > 0 {
			elapsed := time.Since(lastErrTime).Round(time.Second)
			statusText += fmt.Sprintf(" | ✗ DISCONNECTED (attempt %d, %v ago)", attempts, elapsed)
		} else {
			statusText += " | ○ CONNECTING..."
		}
	}

	// Add focus indicator and scroll position
	if state.focusedTable == "near" {
		statusText += fmt.Sprintf(" | Focus: RECENT (row %d-%d of %d)",
			state.nearScrollOffset+1,
			min(state.nearScrollOffset+nearTableHeight-2, len(recentDevices)),
			len(recentDevices))
	} else {
		statusText += fmt.Sprintf(" | Focus: STALE (row %d-%d of %d)",
			state.farScrollOffset+1,
			min(state.farScrollOffset+(availableHeight-nearTableHeight)-2, len(staleDevices)),
			len(staleDevices))
	}
	drawText(s, 0, height-1, width, statusStyle, statusText)

	// Draw recent devices table
	row := 0
	isFocused := state.focusedTable == "near"
	row = drawDeviceTable(s, recentDevices, colWidths, "RECENT DEVICES", row, nearTableHeight, state.nearScrollOffset, isFocused)

	// Draw stale devices table
	isFocused = state.focusedTable == "far"
	row = drawDeviceTable(s, staleDevices, colWidths, "STALE DEVICES", row, availableHeight, state.farScrollOffset, isFocused)

	// Draw disconnection modal overlay if not connected
	if !connected {
		drawDisconnectionModal(s, connState)
	}

	s.Show()
}

// drawDeviceTable renders a single device table with the given title
func drawDeviceTable(s tcell.Screen, devices []*BLEDevice, colWidths []int, title string, startRow int, maxRow int, scrollOffset int, isFocused bool) int {
	width, _ := s.Size()

	// Draw table title with focus indicator
	titleStyle := tcell.StyleDefault.Bold(true).Foreground(tcell.ColorWhite)
	if isFocused {
		titleStyle = titleStyle.Background(tcell.ColorDarkGreen)
	} else {
		titleStyle = titleStyle.Background(tcell.ColorDarkSlateGray)
	}

	titleText := fmt.Sprintf(" %s ", title)
	if isFocused {
		titleText += "◀ FOCUSED"
	}
	drawText(s, 0, startRow, width, titleStyle, titleText)
	startRow++

	// Draw header
	headerStyle := tcell.StyleDefault.Bold(true).Background(tcell.ColorNavy).Foreground(tcell.ColorWhite)
	headers := []string{"Last Seen", "MAC Address", "Sig", "RSSI", "Device Name", "Service UUIDs", "Mfr ID", "Mfr Data"}

	col := 0
	for i, header := range headers {
		drawText(s, col, startRow, colWidths[i], headerStyle, header)
		col += colWidths[i]
	}
	startRow++

	// Calculate available rows for data
	availableRows := maxRow - startRow

	// Clamp scroll offset
	maxScroll := len(devices)
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	if scrollOffset >= maxScroll {
		scrollOffset = max(0, maxScroll-1)
	}

	// Draw devices starting from scrollOffset
	row := startRow
	normalStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)

	for i := scrollOffset; i < len(devices) && row < maxRow; i++ {
		dev := devices[i]

		// Calculate number of lines needed for service UUIDs
		uuidLines := 1
		if len(dev.ServiceUUIDs) > 1 {
			uuidLines = len(dev.ServiceUUIDs)
		}

		// Skip if this device won't fit
		if row+uuidLines > maxRow {
			break
		}

		// Draw Last Seen timestamp (first column)
		lastSeenStr := dev.LastSeen.Format("2006-01-02 15:04:05")
		drawText(s, 0, row, colWidths[0], normalStyle, lastSeenStr)

		// Draw MAC address
		drawText(s, colWidths[0], row, colWidths[1], normalStyle, dev.MacAddress)

		// Draw Signal strength indicator
		signalIndicator, signalColor := getSignalIndicator(dev.RSSI)
		signalStyle := tcell.StyleDefault.Foreground(signalColor).Background(tcell.ColorBlack)
		drawText(s, colWidths[0]+colWidths[1], row, colWidths[2], signalStyle, signalIndicator)

		// Draw RSSI
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2], row, colWidths[3], normalStyle, fmt.Sprintf("%d", dev.RSSI))

		// Draw device name
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2]+colWidths[3], row, colWidths[4], normalStyle, dev.DeviceName)

		// Draw service UUIDs (multi-line with ellipsis support) - now fixed width at 38 chars
		uuidCol := colWidths[0] + colWidths[1] + colWidths[2] + colWidths[3] + colWidths[4]
		if len(dev.ServiceUUIDs) == 0 {
			drawText(s, uuidCol, row, colWidths[5], normalStyle, "")
		} else {
			for j, uuid := range dev.ServiceUUIDs {
				if row+j >= maxRow {
					break
				}
				// Ellipsize if UUID is longer than column width
				displayUUID := uuid
				if len(uuid) > colWidths[5] && colWidths[5] > 3 {
					displayUUID = uuid[:colWidths[5]-3] + "..."
				}
				drawText(s, uuidCol, row+j, colWidths[5], normalStyle, displayUUID)
			}
		}

		// Draw Mfr Code (as integer)
		mfrCodeStr := ""
		if dev.MfrCode != 0 {
			mfrCodeStr = fmt.Sprintf("%d", dev.MfrCode)
		}
		drawText(s, colWidths[0]+colWidths[1]+colWidths[2]+colWidths[3]+colWidths[4]+colWidths[5], row, colWidths[6], normalStyle, mfrCodeStr)

		// Draw Mfr Data (variable width - fills remaining space)
		mfrDataCol := colWidths[0] + colWidths[1] + colWidths[2] + colWidths[3] + colWidths[4] + colWidths[5] + colWidths[6]
		drawText(s, mfrDataCol, row, colWidths[7], normalStyle, dev.MfrData)

		row += uuidLines
	}

	// Draw scroll indicators if needed
	if isFocused && len(devices) > 0 {
		indicatorStyle := tcell.StyleDefault.Foreground(tcell.ColorYellow).Background(tcell.ColorBlack)
		if scrollOffset > 0 {
			// More content above
			drawText(s, width-10, startRow, 10, indicatorStyle, "▲ MORE ▲")
		}
		if scrollOffset+availableRows < len(devices) {
			// More content below
			drawText(s, width-10, maxRow-1, 10, indicatorStyle, "▼ MORE ▼")
		}
	}

	return row
}

// drawText draws text at a specific position
func drawText(s tcell.Screen, x, y, width int, style tcell.Style, text string) {
	// Convert string to runes to properly handle UTF-8 multi-byte characters
	runes := []rune(text)
	col := 0

	// Draw each rune
	for i := 0; i < len(runes) && col < width; i++ {
		s.SetContent(x+col, y, runes[i], nil, style)
		col++
	}

	// Fill remaining space with blanks
	for col < width {
		s.SetContent(x+col, y, ' ', nil, style)
		col++
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// getSignalIndicator returns a visual signal strength indicator based on RSSI
// Returns the indicator string and the color to use
func getSignalIndicator(rssi int) (string, tcell.Color) {
	var bars int
	var color tcell.Color

	// Determine color and number of bars based on RSSI thresholds
	if rssi > -50 {
		// Excellent - Blue - 9 bars
		bars = 7
		color = tcell.ColorBlue
	} else if rssi > -60 {
		// Good - Green - 7 bars
		bars = 5
		color = tcell.ColorGreen
	} else if rssi > -70 {
		// Fair - Yellow - 5 bars
		bars = 3
		color = tcell.ColorYellow
	} else if rssi > -80 {
		// Poor - Orange - 3 bars
		bars = 2
		color = tcell.ColorOrange
	} else {
		// Very Poor - Red - 1 bar
		bars = 1
		color = tcell.ColorRed
	}

	// Build the indicator string using gradient blocks
	// Full block: █ (U+2588) for filled
	// Light shade: ░ (U+2591) for empty
	indicator := ""
	for i := 0; i < bars; i++ {
		indicator += "█"
	}
	for i := bars; i < colWidthSignal-2; i++ {
		indicator += "░"
	}

	return indicator, color
}

// drawDisconnectionModal draws a centered modal overlay showing connection status
func drawDisconnectionModal(s tcell.Screen, connState *ConnectionState) {
	width, height := s.Size()

	// Modal dimensions
	modalWidth := 50
	modalHeight := 8
	modalX := (width - modalWidth) / 2
	modalY := (height - modalHeight) / 2

	// Get connection status
	_, lastErrTime, attempts := connState.GetStatus()
	elapsed := time.Since(lastErrTime).Round(time.Second)

	// Styles
	borderStyle := tcell.StyleDefault.Foreground(tcell.ColorRed).Background(tcell.ColorBlack).Bold(true)
	bgStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkRed)
	textStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkRed)
	buttonStyle := tcell.StyleDefault.Foreground(tcell.ColorBlack).Background(tcell.ColorWhite).Bold(true)

	// Draw background overlay (dim the screen)
	dimStyle := tcell.StyleDefault.Foreground(tcell.ColorGray).Background(tcell.ColorBlack)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if y >= modalY && y < modalY+modalHeight && x >= modalX && x < modalX+modalWidth {
				continue // Skip modal area
			}
			// Dim the background
			mainc, combc, _, _ := s.GetContent(x, y)
			s.SetContent(x, y, mainc, combc, dimStyle)
		}
	}

	// Draw modal background
	for y := modalY; y < modalY+modalHeight; y++ {
		for x := modalX; x < modalX+modalWidth; x++ {
			s.SetContent(x, y, ' ', nil, bgStyle)
		}
	}

	// Draw border
	// Top and bottom borders
	for x := modalX; x < modalX+modalWidth; x++ {
		s.SetContent(x, modalY, '═', nil, borderStyle)
		s.SetContent(x, modalY+modalHeight-1, '═', nil, borderStyle)
	}
	// Side borders
	for y := modalY; y < modalY+modalHeight; y++ {
		s.SetContent(modalX, y, '║', nil, borderStyle)
		s.SetContent(modalX+modalWidth-1, y, '║', nil, borderStyle)
	}
	// Corners
	s.SetContent(modalX, modalY, '╔', nil, borderStyle)
	s.SetContent(modalX+modalWidth-1, modalY, '╗', nil, borderStyle)
	s.SetContent(modalX, modalY+modalHeight-1, '╚', nil, borderStyle)
	s.SetContent(modalX+modalWidth-1, modalY+modalHeight-1, '╝', nil, borderStyle)

	// Draw title
	title := " CONNECTION LOST "
	titleX := modalX + (modalWidth-len(title))/2
	for i, ch := range title {
		s.SetContent(titleX+i, modalY+1, ch, nil, borderStyle)
	}

	// Draw status text
	line1 := "Serial connection interrupted!"
	line2 := fmt.Sprintf("Reconnection attempt: %d", attempts)
	line3 := fmt.Sprintf("Time since last attempt: %v", elapsed)

	drawCenteredText(s, modalX, modalY+3, modalWidth, textStyle, line1)
	drawCenteredText(s, modalX, modalY+4, modalWidth, textStyle, line2)
	drawCenteredText(s, modalX, modalY+5, modalWidth, textStyle, line3)

	// Draw button
	button := " [Q] Quit "
	buttonX := modalX + (modalWidth-len(button))/2
	for i, ch := range button {
		s.SetContent(buttonX+i, modalY+modalHeight-2, ch, nil, buttonStyle)
	}
}

// drawCenteredText draws text centered within a given width
func drawCenteredText(s tcell.Screen, x, y, width int, style tcell.Style, text string) {
	textX := x + (width-len(text))/2
	for i, ch := range text {
		if textX+i >= x && textX+i < x+width {
			s.SetContent(textX+i, y, ch, nil, style)
		}
	}
}

func main() {
	// Command-line flags
	serialPort := flag.String("port", "", "Serial port device (e.g., /dev/ttyUSB0). If not specified, reads from stdin.")
	baudRate := flag.Int("baud", 115200, "Baud rate for serial port (default: 115200)")
	refreshRate := flag.Int("refresh", 4, "TUI refresh rate in updates per second (default: 4)")
	flag.Parse()

	// Calculate refresh interval from refresh rate
	refreshInterval := time.Second / time.Duration(*refreshRate)

	// Initialize aggregator
	agg := NewAggregator()

	// Paused state
	var paused bool
	var pauseMu sync.RWMutex

	// Done channel for graceful shutdown
	done := make(chan struct{})

	// Initialize connection state
	connState := &ConnectionState{
		connected: false,
	}

	// Start reading from input source (handles reconnection internally)
	go readSerial(*serialPort, *baudRate, agg, &paused, &pauseMu, connState, done)

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
	s.EnableMouse() // Enable mouse support for scrolling

	// Initialize table state
	tableState := &TableState{
		nearScrollOffset: 0,
		farScrollOffset:  0,
		focusedTable:     "near",
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Ticker for refresh
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	// Initial draw
	drawTable(s, agg.GetSorted(), paused, tableState, connState)

	// Event loop
	quit := false
	for !quit {
		select {
		case <-ticker.C:
			pauseMu.RLock()
			isPaused := paused
			pauseMu.RUnlock()
			drawTable(s, agg.GetSorted(), isPaused, tableState, connState)

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
							// Export with timestamp in filename
							timestamp := time.Now().Format("2006-01-02_15-04-05")
							filename := fmt.Sprintf("ble_devices_%s.json", timestamp)
							if err := agg.ExportJSON(filename); err != nil {
								// Could show error in status line, but for now ignore
							}
						case 'c', 'C':
							// Clear the aggregator
							agg.Clear()
							// Reset scroll positions
							tableState.nearScrollOffset = 0
							tableState.farScrollOffset = 0
							drawTable(s, agg.GetSorted(), paused, tableState, connState)
						case 'p', 'P':
							pauseMu.Lock()
							paused = !paused
							pauseMu.Unlock()
						case 'j', 'J': // Scroll down (vim-style)
							if tableState.focusedTable == "near" {
								tableState.nearScrollOffset++
							} else {
								tableState.farScrollOffset++
							}
							drawTable(s, agg.GetSorted(), paused, tableState, connState)
						case 'k', 'K': // Scroll up (vim-style)
							if tableState.focusedTable == "near" {
								tableState.nearScrollOffset--
								if tableState.nearScrollOffset < 0 {
									tableState.nearScrollOffset = 0
								}
							} else {
								tableState.farScrollOffset--
								if tableState.farScrollOffset < 0 {
									tableState.farScrollOffset = 0
								}
							}
							drawTable(s, agg.GetSorted(), paused, tableState, connState)
						}
					case tcell.KeyUp:
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset--
							if tableState.nearScrollOffset < 0 {
								tableState.nearScrollOffset = 0
							}
						} else {
							tableState.farScrollOffset--
							if tableState.farScrollOffset < 0 {
								tableState.farScrollOffset = 0
							}
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyDown:
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset++
						} else {
							tableState.farScrollOffset++
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyPgUp:
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset -= 10
							if tableState.nearScrollOffset < 0 {
								tableState.nearScrollOffset = 0
							}
						} else {
							tableState.farScrollOffset -= 10
							if tableState.farScrollOffset < 0 {
								tableState.farScrollOffset = 0
							}
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyPgDn:
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset += 10
						} else {
							tableState.farScrollOffset += 10
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyHome:
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset = 0
						} else {
							tableState.farScrollOffset = 0
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyEnd:
						devices := agg.GetSorted()
						if tableState.focusedTable == "near" {
							tableState.nearScrollOffset = len(devices)
						} else {
							tableState.farScrollOffset = len(devices)
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyTab:
						// Switch focus between tables
						if tableState.focusedTable == "near" {
							tableState.focusedTable = "far"
						} else {
							tableState.focusedTable = "near"
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					case tcell.KeyCtrlC:
						quit = true
					}
				case *tcell.EventMouse:
					// Handle mouse scroll events
					_, y := ev.Position()
					buttons := ev.Buttons()

					// Determine which table the mouse is over
					// (This is a simplified version - you may want to track exact table boundaries)
					_, height := s.Size()
					midPoint := (height - 1) / 2

					if buttons&tcell.WheelUp != 0 {
						// Scroll up
						if y < midPoint && tableState.focusedTable == "near" {
							tableState.nearScrollOffset--
							if tableState.nearScrollOffset < 0 {
								tableState.nearScrollOffset = 0
							}
						} else if y >= midPoint && tableState.focusedTable == "far" {
							tableState.farScrollOffset--
							if tableState.farScrollOffset < 0 {
								tableState.farScrollOffset = 0
							}
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					} else if buttons&tcell.WheelDown != 0 {
						// Scroll down
						if y < midPoint && tableState.focusedTable == "near" {
							tableState.nearScrollOffset++
						} else if y >= midPoint && tableState.focusedTable == "far" {
							tableState.farScrollOffset++
						}
						drawTable(s, agg.GetSorted(), paused, tableState, connState)
					}
				case *tcell.EventResize:
					s.Sync()
					pauseMu.RLock()
					isPaused := paused
					pauseMu.RUnlock()
					drawTable(s, agg.GetSorted(), isPaused, tableState, connState)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(done)
}
