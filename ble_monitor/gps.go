package main

import (
	"bufio"
	"io"
	"time"

	"github.com/adrianmo/go-nmea"
	"go.bug.st/serial"
)

// GPS baud rates to try, in order of likelihood
var gpsBaudRates = []int{9600, 115200, 38400, 4800}

// autoBaudDetect attempts to detect the correct baud rate for the GPS device
// Returns the detected baud rate, or 0 if detection failed
func autoBaudDetect(portPath string) int {
	const detectionWindow = 2 * time.Second
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		for _, baudRate := range gpsBaudRates {
			// Try to open at this baud rate
			port, err := openGPSPort(portPath, baudRate)
			if err != nil {
				continue
			}

			// Try to read valid NMEA sentences
			if detectValidNMEA(port, detectionWindow) {
				port.Close()
				return baudRate
			}

			port.Close()
		}
	}

	return 0 // Detection failed
}

// openGPSPort opens a GPS serial port with the given baud rate
func openGPSPort(portPath string, baudRate int) (io.ReadWriteCloser, error) {
	mode := &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	return serial.Open(portPath, mode)
}

// detectValidNMEA tries to read valid NMEA sentences within the given duration
func detectValidNMEA(port io.Reader, duration time.Duration) bool {
	scanner := bufio.NewScanner(port)
	deadline := time.Now().Add(duration)

	validCount := 0
	for time.Now().Before(deadline) {
		// Set a small timeout for scanning
		if !scanner.Scan() {
			break
		}

		line := scanner.Text()

		// Try to parse as NMEA
		_, err := nmea.Parse(line)
		if err == nil {
			validCount++
			// If we get 2+ valid sentences, consider it detected
			if validCount >= 2 {
				return true
			}
		}
	}

	return false
}

// readGPS reads GPS/GNSS data from a serial port and updates location state
// Supports automatic reconnection with exponential backoff
func readGPS(portPath string, locState *LocationState, done <-chan struct{}) {
	var port io.ReadWriteCloser
	var err error

	// Set status to detecting
	locState.SetStatus("detecting")

	// Auto-detect baud rate initially
	baudRate := autoBaudDetect(portPath)
	if baudRate == 0 {
		// Detection failed
		locState.SetStatus("failed")
		return
	}

	// Reconnection logic with exponential backoff
	reconnectDelay := 1 * time.Second
	maxReconnectDelay := 5 * time.Second

	for {
		select {
		case <-done:
			if port != nil {
				port.Close()
			}
			return
		default:
		}

		// Attempt to open/reopen the GPS port
		port, err = openGPSPort(portPath, baudRate)
		if err != nil {
			// Failed to open, increment reconnect attempt
			locState.SetGPSReconnectAttempt()
			locState.SetGPSConnected(false)
			locState.SetStatus("no_fix")

			select {
			case <-done:
				return
			case <-time.After(reconnectDelay):
				// Linear backoff
				reconnectDelay += 1 * time.Second
				if reconnectDelay > maxReconnectDelay {
					reconnectDelay = maxReconnectDelay
				}
			}
			continue
		}

		// Successfully opened
		locState.SetGPSConnected(true)
		locState.SetStatus("no_fix")
		reconnectDelay = 1 * time.Second // Reset backoff

		// Read from the port until error or done
		err = readGPSLoop(port, locState, done)

		// Close the port
		port.Close()

		// If we're done, exit
		select {
		case <-done:
			return
		default:
		}

		// Connection lost, mark as disconnected
		locState.SetGPSConnected(false)
		locState.SetStatus("no_fix")

		// Brief delay before reconnect attempt
		select {
		case <-done:
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// readGPSLoop performs the actual GPS reading and processing
func readGPSLoop(port io.Reader, locState *LocationState, done <-chan struct{}) error {
	scanner := bufio.NewScanner(port)
	scanner.Buffer(make([]byte, 4096), 16384)

	// Track satellites in view from GSV messages
	// We need to accumulate across multiple GSV sentences
	gsvSatellitesInView := 0

	for {
		select {
		case <-done:
			return nil
		default:
			if scanner.Scan() {
				line := scanner.Text()
				parseNMEASentence(line, locState, &gsvSatellitesInView)
			} else {
				// Error or EOF
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

// parseNMEASentence parses an NMEA sentence and updates location state
func parseNMEASentence(line string, locState *LocationState, gsvSatellitesInView *int) {
	s, err := nmea.Parse(line)
	if err != nil {
		// Ignore malformed sentences
		return
	}

	// Handle different sentence types
	switch m := s.(type) {
	case nmea.GGA:
		// GGA: Global Positioning System Fix Data
		// Preferred for elevation data
		handleGGA(m, locState, *gsvSatellitesInView)

	case nmea.RMC:
		// RMC: Recommended Minimum Navigation Information
		// Use as fallback if GGA not available
		handleRMC(m, locState, *gsvSatellitesInView)

	case nmea.GSV:
		// GSV: Satellites in View
		// Track total satellites in view across all constellations
		handleGSV(m, gsvSatellitesInView)
	}
}

// handleGGA processes a GGA sentence (position, elevation, fix quality)
func handleGGA(gga nmea.GGA, locState *LocationState, satellitesInView int) {
	// Parse fix quality
	fixQuality := parseFixQuality(gga.FixQuality)

	if fixQuality == 0 {
		// No fix
		locState.SetStatus("no_fix")
		return
	}

	// Valid fix - create GeoLocation
	loc := &GeoLocation{
		Latitude:  gga.Latitude,
		Longitude: gga.Longitude,
		Elevation: gga.Altitude,
		Accuracy:  gga.HDOP, // Horizontal Dilution of Precision
		Timestamp: time.Now().UTC(),
	}

	locState.SetCurrent(loc, fixQuality, int(gga.NumSatellites), satellitesInView)
}

// handleRMC processes an RMC sentence (position, speed, date)
func handleRMC(rmc nmea.RMC, locState *LocationState, satellitesInView int) {
	// Only use if valid
	if rmc.Validity != "A" {
		locState.SetStatus("no_fix")
		return
	}

	// RMC doesn't have elevation or satellites, so use 0/unknown
	loc := &GeoLocation{
		Latitude:  rmc.Latitude,
		Longitude: rmc.Longitude,
		Elevation: 0, // RMC doesn't provide elevation
		Accuracy:  0, // RMC doesn't provide HDOP
		Timestamp: time.Now().UTC(),
	}

	// Set with minimal fix quality (1 = GPS fix) and unknown satellite counts
	locState.SetCurrent(loc, 1, 0, satellitesInView)
}

// handleGSV processes a GSV sentence (satellites in view)
func handleGSV(gsv nmea.GSV, gsvSatellitesInView *int) {
	// GSV sentences come in multiple messages
	// TotalMessages tells us how many total messages
	// MessageNumber tells us which message this is
	// NumberSVsInView is only present in the first message

	// If this is the first message of a new sequence, reset the counter
	if gsv.MessageNumber == 1 {
		*gsvSatellitesInView = int(gsv.NumberSVsInView)
	}
	// Note: We don't need to accumulate across messages because
	// NumberSVsInView in the first message already gives us the total
}

// parseFixQuality converts NMEA fix quality string to integer
func parseFixQuality(quality string) int {
	switch quality {
	case "0":
		return 0 // Invalid
	case "1":
		return 1 // GPS fix
	case "2":
		return 2 // DGPS fix
	case "3":
		return 3 // PPS fix
	case "4":
		return 4 // RTK fix
	case "5":
		return 5 // RTK float
	case "6":
		return 6 // Estimated
	default:
		return 0
	}
}
