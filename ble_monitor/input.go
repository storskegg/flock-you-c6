package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// handleKeyboardEvent processes keyboard input
func handleKeyboardEvent(ev *tcell.EventKey, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex, tableState *TableState, connState *ConnectionState, s tcell.Screen) bool {
	switch ev.Key() {
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'q', 'Q':
			return true // Signal quit
		case 'e', 'E':
			handleExport(agg)
		case 'c', 'C':
			handleClear(agg, tableState, paused, s, connState)
		case 'p', 'P':
			handlePause(paused, pauseMu)
		case 'j', 'J': // Scroll down (vim-style)
			handleScrollDown(tableState)
			drawTable(s, agg.GetSorted(), *paused, tableState, connState)
		case 'k', 'K': // Scroll up (vim-style)
			handleScrollUp(tableState)
			drawTable(s, agg.GetSorted(), *paused, tableState, connState)
		}
	case tcell.KeyUp:
		handleScrollUp(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyDown:
		handleScrollDown(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyPgUp:
		handlePageUp(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyPgDn:
		handlePageDown(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyHome:
		handleHome(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyEnd:
		handleEnd(tableState, agg)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyTab:
		handleTabSwitch(tableState)
		drawTable(s, agg.GetSorted(), *paused, tableState, connState)
	case tcell.KeyCtrlC:
		return true // Signal quit
	}
	return false
}

// handleExport exports devices to timestamped JSON file
func handleExport(agg *Aggregator) {
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	filename := fmt.Sprintf("ble_devices_%s.json", timestamp)
	agg.ExportJSON(filename)
	// Could show error in status line, but for now ignore
}

// handleClear clears the aggregator and resets scroll positions
func handleClear(agg *Aggregator, tableState *TableState, paused *bool, s tcell.Screen, connState *ConnectionState) {
	agg.Clear()
	tableState.nearScrollOffset = 0
	tableState.farScrollOffset = 0
	drawTable(s, agg.GetSorted(), *paused, tableState, connState)
}

// handlePause toggles pause state
func handlePause(paused *bool, pauseMu *sync.RWMutex) {
	pauseMu.Lock()
	*paused = !*paused
	pauseMu.Unlock()
}

// handleScrollDown scrolls the focused table down by one row
func handleScrollDown(tableState *TableState) {
	if tableState.focusedTable == "near" {
		tableState.nearScrollOffset++
	} else {
		tableState.farScrollOffset++
	}
}

// handleScrollUp scrolls the focused table up by one row
func handleScrollUp(tableState *TableState) {
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
}

// handlePageUp scrolls the focused table up by 10 rows
func handlePageUp(tableState *TableState) {
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
}

// handlePageDown scrolls the focused table down by 10 rows
func handlePageDown(tableState *TableState) {
	if tableState.focusedTable == "near" {
		tableState.nearScrollOffset += 10
	} else {
		tableState.farScrollOffset += 10
	}
}

// handleHome scrolls the focused table to the top
func handleHome(tableState *TableState) {
	if tableState.focusedTable == "near" {
		tableState.nearScrollOffset = 0
	} else {
		tableState.farScrollOffset = 0
	}
}

// handleEnd scrolls the focused table to the bottom
func handleEnd(tableState *TableState, agg *Aggregator) {
	sorted := agg.GetSorted()
	if tableState.focusedTable == "near" {
		tableState.nearScrollOffset = len(sorted.Recent)
	} else {
		tableState.farScrollOffset = len(sorted.Stale)
	}
}

// handleTabSwitch switches focus between tables
func handleTabSwitch(tableState *TableState) {
	if tableState.focusedTable == "near" {
		tableState.focusedTable = "far"
	} else {
		tableState.focusedTable = "near"
	}
}

// handleMouseEvent processes mouse input
func handleMouseEvent(ev *tcell.EventMouse, tableState *TableState, agg *Aggregator, paused bool, s tcell.Screen, connState *ConnectionState) {
	_, y := ev.Position()
	buttons := ev.Buttons()

	// Determine which table the mouse is over
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
}

// handleResizeEvent processes terminal resize events
func handleResizeEvent(s tcell.Screen, agg *Aggregator, paused *bool, pauseMu *sync.RWMutex, tableState *TableState, connState *ConnectionState) {
	s.Sync()
	pauseMu.RLock()
	isPaused := *paused
	pauseMu.RUnlock()
	drawTable(s, agg.GetSorted(), isPaused, tableState, connState)
}
