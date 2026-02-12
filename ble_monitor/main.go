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
	gpsPort := flag.String("gps", "", "GPS/GNSS serial port device (e.g., /dev/ttyUSB1). If not specified, no GPS data collected.")
	mergeKML := flag.Bool("merge-kml", false, "Merge KML files and exit. Provide KML files as remaining arguments.")
	flag.Parse()

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

	// Initialize location state
	locState := NewLocationState()

	// Start GPS reading if -gps flag is provided
	if *gpsPort != "" {
		go readGPS(*gpsPort, locState, done)
	}

	// Start reading from input source (handles reconnection internally)
	go readSerial(*serialPort, *baudRate, agg, &paused, &pauseMu, connState, locState, done)

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
	drawTable(s, agg.GetSorted(), paused, tableState, connState, locState, exportModal)

	// Event loop
	quit := false
	for !quit {
		select {
		case <-ticker.C:
			pauseMu.RLock()
			isPaused := paused
			pauseMu.RUnlock()
			drawTable(s, agg.GetSorted(), isPaused, tableState, connState, locState, exportModal)

		case <-sigChan:
			quit = true

		default:
			// Check for key events (non-blocking)
			if s.HasPendingEvent() {
				ev := s.PollEvent()
				switch ev := ev.(type) {
				case *tcell.EventKey:
					if handleKeyboardEvent(ev, agg, &paused, &pauseMu, tableState, connState, locState, exportModal, s) {
						quit = true
					}
				case *tcell.EventMouse:
					handleMouseEvent(ev, tableState, agg, paused, s, connState, locState, exportModal)
				case *tcell.EventResize:
					handleResizeEvent(s, agg, &paused, &pauseMu, tableState, connState, locState, exportModal)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(done)
}
