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
	mergeKML := flag.Bool("merge-kml", false, "Merge KML files and exit. Provide KML files as remaining arguments.")
	updateKML := flag.String("update-kml", "", "Update existing KML file with new features (styling, etc.) and save in place.")
	flag.Parse()

	// Handle update-kml mode (update and exit, no TUI)
	if *updateKML != "" {
		if err := updateKMLAndExit(*updateKML); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating KML file: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Handle merge-kml mode (merge and exit, no TUI)
	if *mergeKML {
		// Remaining args are the KML files to merge
		kmlFiles := flag.Args()
		if len(kmlFiles) == 0 {
			fmt.Fprintf(os.Stderr, "Error: -merge-kml requires at least one KML file argument\n")
			fmt.Fprintf(os.Stderr, "Usage: %s -merge-kml file1.kml file2.kml file3.kml\n", os.Args[0])
			os.Exit(1)
		}

		if err := mergeKMLAndExit(kmlFiles); err != nil {
			fmt.Fprintf(os.Stderr, "Error merging KML files: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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

	// Initialize export modal state
	exportModal := &ExportModalState{
		showing:        false,
		selectedOption: 0,
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Ticker for refresh
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	// Initial draw
	drawTable(s, agg.GetSorted(), paused, tableState, connState, exportModal)

	// Event loop
	quit := false
	for !quit {
		select {
		case <-ticker.C:
			pauseMu.RLock()
			isPaused := paused
			pauseMu.RUnlock()
			drawTable(s, agg.GetSorted(), isPaused, tableState, connState, exportModal)

		case <-sigChan:
			quit = true

		default:
			// Check for key events (non-blocking)
			if s.HasPendingEvent() {
				ev := s.PollEvent()
				switch ev := ev.(type) {
				case *tcell.EventKey:
					if handleKeyboardEvent(ev, agg, &paused, &pauseMu, tableState, connState, exportModal, s) {
						quit = true
					}
				case *tcell.EventMouse:
					handleMouseEvent(ev, tableState, agg, paused, s, connState, exportModal)
				case *tcell.EventResize:
					handleResizeEvent(s, agg, &paused, &pauseMu, tableState, connState, exportModal)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(done)
}
