package main

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
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

// TableState tracks scrolling and focus state for the tables
type TableState struct {
	nearScrollOffset int
	farScrollOffset  int
	focusedTable     string // "near" or "far"
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
		// Excellent - Blue - 7 bars
		bars = 7
		color = tcell.ColorBlue
	} else if rssi > -60 {
		// Good - Green - 5 bars
		bars = 5
		color = tcell.ColorGreen
	} else if rssi > -70 {
		// Fair - Yellow - 3 bars
		bars = 3
		color = tcell.ColorYellow
	} else if rssi > -80 {
		// Poor - Orange - 2 bars
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
