package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
)

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
					if handleKeyboardEvent(ev, agg, &paused, &pauseMu, tableState, connState, s) {
						quit = true
					}
				case *tcell.EventMouse:
					handleMouseEvent(ev, tableState, agg, paused, s, connState)
				case *tcell.EventResize:
					handleResizeEvent(s, agg, &paused, &pauseMu, tableState, connState)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(done)
}
