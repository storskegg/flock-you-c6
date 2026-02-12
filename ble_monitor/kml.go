package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/twpayne/go-kml/v3"
)

// buildDeviceDescription creates HTML description for device metadata
// Matches the TUI table column order
func buildDeviceDescription(dev *BLEDevice) string {
	var html strings.Builder

	html.WriteString("<ul>")

	// Last Seen
	html.WriteString("<li><strong>Last Seen:</strong> ")
	html.WriteString(dev.LastSeen.Format("2006-01-02 15:04:05"))
	html.WriteString("</li>")

	// Count
	html.WriteString("<li><strong>Count:</strong> ")
	html.WriteString(fmt.Sprintf("%d", dev.Count))
	html.WriteString("</li>")

	// MAC Address
	html.WriteString("<li><strong>MAC Address:</strong> ")
	html.WriteString(dev.MacAddress)
	html.WriteString("</li>")

	// Signal (show RSSI in dBm)
	html.WriteString("<li><strong>Signal:</strong> ")
	html.WriteString(fmt.Sprintf("%d dBm", dev.RSSI))
	html.WriteString("</li>")

	// RSSI
	html.WriteString("<li><strong>RSSI:</strong> ")
	html.WriteString(fmt.Sprintf("%d", dev.RSSI))
	html.WriteString("</li>")

	// Location
	if dev.GeoData != nil {
		if loc := dev.GeoData.GetLocation(); loc != nil {
			html.WriteString("<li><strong>Location:</strong> ")
			html.WriteString(fmt.Sprintf("%.5f, %.5f", loc.Latitude, loc.Longitude))
			html.WriteString("</li>")
		}
	}

	// Device Name
	html.WriteString("<li><strong>Device Name:</strong> ")
	if dev.DeviceName != "" {
		html.WriteString(dev.DeviceName)
	} else {
		html.WriteString("(unnamed)")
	}
	html.WriteString("</li>")

	// Service UUIDs
	html.WriteString("<li><strong>Service UUIDs:</strong> ")
	if len(dev.ServiceUUIDs) > 0 {
		html.WriteString(strings.Join(dev.ServiceUUIDs, ", "))
	} else {
		html.WriteString("(none)")
	}
	html.WriteString("</li>")

	// Manufacturer Code
	html.WriteString("<li><strong>Mfr ID:</strong> ")
	if dev.MfrCode != 0 {
		html.WriteString(fmt.Sprintf("%d", dev.MfrCode))
	} else {
		html.WriteString("(none)")
	}
	html.WriteString("</li>")

	// Manufacturer Data
	html.WriteString("<li><strong>Mfr Data:</strong> ")
	if dev.MfrData != "" {
		html.WriteString(dev.MfrData)
	} else {
		html.WriteString("(none)")
	}
	html.WriteString("</li>")

	html.WriteString("</ul>")

	return html.String()
}

// computeConvexHull computes the convex hull of a set of points using Graham scan
// This ensures we only draw convex polygons even with 3+ points
func computeConvexHull(points []GeoLocation) []GeoLocation {
	if len(points) < 3 {
		return points
	}

	// For 3 points, just check if they're in counter-clockwise order
	if len(points) == 3 {
		return ensureCounterClockwise(points)
	}

	// Make a copy to avoid modifying the input slice
	pointsCopy := make([]GeoLocation, len(points))
	copy(pointsCopy, points)

	// For more points, implement Graham scan
	// Find the point with lowest latitude (and leftmost if tie)
	lowestIdx := 0
	for i := 1; i < len(pointsCopy); i++ {
		if pointsCopy[i].Latitude < pointsCopy[lowestIdx].Latitude ||
			(pointsCopy[i].Latitude == pointsCopy[lowestIdx].Latitude && pointsCopy[i].Longitude < pointsCopy[lowestIdx].Longitude) {
			lowestIdx = i
		}
	}

	// Swap lowest point to position 0
	pointsCopy[0], pointsCopy[lowestIdx] = pointsCopy[lowestIdx], pointsCopy[0]
	pivot := pointsCopy[0]

	// Sort remaining points by polar angle with respect to pivot
	remaining := pointsCopy[1:]
	if len(remaining) == 0 {
		// Only one point total
		return []GeoLocation{pivot}
	}

	sortByPolarAngle(remaining, pivot)

	// Build convex hull
	hull := []GeoLocation{pivot, remaining[0]}

	for i := 1; i < len(remaining); i++ {
		// Remove points that make clockwise turn
		for len(hull) > 1 && !isCounterClockwise(hull[len(hull)-2], hull[len(hull)-1], remaining[i]) {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, remaining[i])
	}

	return hull
}

// ensureCounterClockwise ensures 3 points are in counter-clockwise order
func ensureCounterClockwise(points []GeoLocation) []GeoLocation {
	if len(points) != 3 {
		return points
	}

	if !isCounterClockwise(points[0], points[1], points[2]) {
		// Swap to make counter-clockwise
		return []GeoLocation{points[0], points[2], points[1]}
	}

	return points
}

// isCounterClockwise checks if three points make a counter-clockwise turn
func isCounterClockwise(p1, p2, p3 GeoLocation) bool {
	return (p2.Longitude-p1.Longitude)*(p3.Latitude-p1.Latitude)-
		(p2.Latitude-p1.Latitude)*(p3.Longitude-p1.Longitude) > 0
}

// sortByPolarAngle sorts points by polar angle relative to pivot (in place)
func sortByPolarAngle(points []GeoLocation, pivot GeoLocation) {
	// Simple insertion sort by angle (good enough for small N)
	for i := 1; i < len(points); i++ {
		key := points[i]
		j := i - 1

		for j >= 0 && polarAngle(pivot, points[j]) > polarAngle(pivot, key) {
			points[j+1] = points[j]
			j--
		}
		points[j+1] = key
	}
}

// polarAngle computes the polar angle from pivot to point
func polarAngle(pivot, point GeoLocation) float64 {
	dy := point.Latitude - pivot.Latitude
	dx := point.Longitude - pivot.Longitude

	// Handle special cases to avoid division by zero
	if dx == 0 && dy == 0 {
		return 0 // Same point
	}
	if dx == 0 {
		if dy > 0 {
			return 1e9 // Vertical up (very large angle)
		}
		return -1e9 // Vertical down
	}
	return dy / dx // Simplified comparison for sorting purposes
}

// smoothPath applies Ramer-Douglas-Peucker algorithm to simplify/smooth a path
// Reduces visual noise while preserving the overall shape
func smoothPath(points []GeoLocation) []GeoLocation {
	if len(points) <= 2 {
		return points
	}

	// Epsilon controls how much simplification occurs
	// Larger epsilon = more simplification
	// This is in degrees; ~0.0001 degrees ≈ 11 meters at equator
	const epsilon = 0.0001

	return douglasPeucker(points, epsilon)
}

// douglasPeucker implements the Ramer-Douglas-Peucker algorithm for path simplification
func douglasPeucker(points []GeoLocation, epsilon float64) []GeoLocation {
	if len(points) <= 2 {
		return points
	}

	// Find the point with maximum distance from the line segment
	dmax := 0.0
	index := 0
	end := len(points) - 1

	for i := 1; i < end; i++ {
		d := perpendicularDistance(points[i], points[0], points[end])
		if d > dmax {
			index = i
			dmax = d
		}
	}

	// If max distance is greater than epsilon, recursively simplify
	if dmax > epsilon {
		// Recursive call on both segments
		left := douglasPeucker(points[:index+1], epsilon)
		right := douglasPeucker(points[index:], epsilon)

		// Combine results (remove duplicate middle point)
		result := make([]GeoLocation, 0, len(left)+len(right)-1)
		result = append(result, left...)
		result = append(result, right[1:]...)
		return result
	}

	// Max distance is less than epsilon, return just endpoints
	return []GeoLocation{points[0], points[end]}
}

// perpendicularDistance calculates the perpendicular distance from point to line segment
func perpendicularDistance(point, lineStart, lineEnd GeoLocation) float64 {
	// Using simplified 2D distance for lat/lon (good enough for small distances)
	x := point.Longitude
	y := point.Latitude
	x1 := lineStart.Longitude
	y1 := lineStart.Latitude
	x2 := lineEnd.Longitude
	y2 := lineEnd.Latitude

	dx := x2 - x1
	dy := y2 - y1

	// Handle degenerate case where line segment is a point
	if dx == 0 && dy == 0 {
		// Distance to point
		return ((x-x1)*(x-x1) + (y-y1)*(y-y1))
	}

	// Calculate perpendicular distance using cross product
	numerator := ((y2-y1)*x - (x2-x1)*y + x2*y1 - y2*x1)
	if numerator < 0 {
		numerator = -numerator
	}
	denominator := (dx*dx + dy*dy)

	if denominator == 0 {
		return 0
	}

	// Return normalized distance
	return (numerator * numerator) / denominator
}

// createPlacemarksForDevice creates KML placemarks for a device
// Returns up to 3 placemarks: point, path, polygon

// ExportKML exports all devices with geolocation data to a KML file
// Organized into layers: Points, Paths, Polygons, and Session Boundary
func (a *Aggregator) ExportKML(filename string) error {
	sorted := a.GetSorted()

	// Combine all devices (recent first, then stale)
	allDevices := make([]*BLEDevice, 0, len(sorted.Recent)+len(sorted.Stale))
	allDevices = append(allDevices, sorted.Recent...)
	allDevices = append(allDevices, sorted.Stale...)

	// Separate placemarks by type (layer)
	var pointPlacemarks []kml.Element
	var pathPlacemarks []kml.Element
	var polygonPlacemarks []kml.Element
	var allPoints []GeoLocation // Collect all points for session boundary

	for _, dev := range allDevices {
		if dev.GeoData == nil {
			continue
		}

		// Get location data from all RSSIs
		dev.GeoData.mu.RLock()

		if len(dev.GeoData.allRSSIs) == 0 {
			dev.GeoData.mu.RUnlock()
			continue
		}

		// For points: use only the highest RSSI
		highestRSSI := dev.GeoData.allRSSIs[0]
		highestBuffer := dev.GeoData.data[highestRSSI]

		var highestLocations []GeoLocation
		if highestBuffer != nil && highestBuffer.Size() > 0 {
			highestLocations = highestBuffer.GetAll()
		}

		// For paths and polygons: collect ALL locations from ALL RSSIs
		var allDeviceLocations []GeoLocation
		for _, rssi := range dev.GeoData.allRSSIs {
			buffer := dev.GeoData.data[rssi]
			if buffer != nil && buffer.Size() > 0 {
				locations := buffer.GetAll()
				allDeviceLocations = append(allDeviceLocations, locations...)
			}
		}

		dev.GeoData.mu.RUnlock()

		// Skip if we have no data at all
		if len(highestLocations) == 0 && len(allDeviceLocations) == 0 {
			continue
		}

		// Collect all points for session boundary
		allPoints = append(allPoints, allDeviceLocations...)

		description := buildDeviceDescription(dev)

		// Calculate average location from highest RSSI only
		var avgLoc *GeoLocation
		if len(highestLocations) > 0 {
			var sumLat, sumLon, sumEl float64
			for _, loc := range highestLocations {
				sumLat += loc.Latitude
				sumLon += loc.Longitude
				sumEl += loc.Elevation
			}
			count := float64(len(highestLocations))
			avgLoc = &GeoLocation{
				Latitude:  sumLat / count,
				Longitude: sumLon / count,
				Elevation: sumEl / count,
			}
		}

		// 1. Point (if at least 1 location in highest RSSI)
		if avgLoc != nil {
			pointPlacemarks = append(pointPlacemarks, kml.Placemark(
				kml.Name(dev.MacAddress),
				kml.Description(description),
				kml.Point(
					kml.Coordinates(kml.Coordinate{
						Lon: avgLoc.Longitude,
						Lat: avgLoc.Latitude,
						Alt: avgLoc.Elevation,
					}),
				),
			))
		}

		// 2. Path (if at least 2 locations across ALL RSSIs)
		// Apply smoothing to reduce visual noise
		if len(allDeviceLocations) >= 2 {
			smoothedPath := smoothPath(allDeviceLocations)
			coords := make([]kml.Coordinate, len(smoothedPath))
			for i, loc := range smoothedPath {
				coords[i] = kml.Coordinate{
					Lon: loc.Longitude,
					Lat: loc.Latitude,
					Alt: loc.Elevation,
				}
			}

			pathPlacemarks = append(pathPlacemarks, kml.Placemark(
				kml.Name(dev.MacAddress),
				kml.Description(description),
				kml.LineString(
					kml.Coordinates(coords...),
				),
			))
		}

		// 3. Polygon (if at least 3 locations across ALL RSSIs)
		if len(allDeviceLocations) >= 3 {
			// Compute convex hull to ensure we draw a proper polygon
			hull := computeConvexHull(allDeviceLocations)

			// Convert hull to coordinates (and close the polygon)
			coords := make([]kml.Coordinate, len(hull)+1)
			for i, loc := range hull {
				coords[i] = kml.Coordinate{
					Lon: loc.Longitude,
					Lat: loc.Latitude,
					Alt: loc.Elevation,
				}
			}
			// Close the polygon by repeating the first point
			coords[len(hull)] = coords[0]

			polygonPlacemarks = append(polygonPlacemarks, kml.Placemark(
				kml.Name(dev.MacAddress),
				kml.Description(description),
				kml.Polygon(
					kml.OuterBoundaryIs(
						kml.LinearRing(
							kml.Coordinates(coords...),
						),
					),
				),
			))
		}
	}

	// Build document elements
	docElements := []kml.Element{
		kml.Name(fmt.Sprintf("BLE Devices - %s", time.Now().Format("2006-01-02 15:04:05"))),
	}

	// Add Points folder
	if len(pointPlacemarks) > 0 {
		pointsFolderElements := []kml.Element{kml.Name("Points")}
		pointsFolderElements = append(pointsFolderElements, pointPlacemarks...)
		docElements = append(docElements, kml.Folder(pointsFolderElements...))
	}

	// Add Paths folder
	if len(pathPlacemarks) > 0 {
		pathsFolderElements := []kml.Element{kml.Name("Paths")}
		pathsFolderElements = append(pathsFolderElements, pathPlacemarks...)
		docElements = append(docElements, kml.Folder(pathsFolderElements...))
	}

	// Add Polygons folder
	if len(polygonPlacemarks) > 0 {
		polygonsFolderElements := []kml.Element{kml.Name("Polygons")}
		polygonsFolderElements = append(polygonsFolderElements, polygonPlacemarks...)
		docElements = append(docElements, kml.Folder(polygonsFolderElements...))
	}

	// Add Session Boundary folder (if we have any points)
	if len(allPoints) > 0 {
		sessionBoundary := createSessionBoundary(allPoints)
		if sessionBoundary != nil {
			sessionFolderElements := []kml.Element{
				kml.Name("Session Boundary"),
				sessionBoundary,
			}
			docElements = append(docElements, kml.Folder(sessionFolderElements...))
		}
	}

	// Create KML document
	doc := kml.KML(
		kml.Document(docElements...),
	)

	// Create file
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Write KML
	if err := doc.WriteIndent(file, "", "  "); err != nil {
		return fmt.Errorf("failed to write KML: %w", err)
	}

	return nil
}

// mergeKMLAndExit merges multiple KML files and writes the result
// Called from main when -merge-kml flag is used
func mergeKMLAndExit(filePaths []string) error {
	if len(filePaths) == 0 {
		return fmt.Errorf("no files specified")
	}

	fmt.Printf("Merging %d KML files...\n", len(filePaths))

	// Collect all placemarks by folder type
	var allPoints []string
	var allPaths []string
	var allPolygons []string
	var allSessionPoints []GeoLocation

	successCount := 0

	// Process each file
	for _, filePath := range filePaths {
		fmt.Printf("Reading: %s\n", filePath)

		points, paths, polygons, sessionPoints, err := extractPlacemarksFromKML(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Failed to parse %s: %v (skipping)\n", filePath, err)
			continue
		}

		allPoints = append(allPoints, points...)
		allPaths = append(allPaths, paths...)
		allPolygons = append(allPolygons, polygons...)
		allSessionPoints = append(allSessionPoints, sessionPoints...)

		successCount++
		fmt.Printf("  ✓ Loaded %d points, %d paths, %d polygons\n", len(points), len(paths), len(polygons))
	}

	if successCount == 0 {
		return fmt.Errorf("no files successfully parsed")
	}

	fmt.Printf("\nSuccessfully merged %d/%d files\n", successCount, len(filePaths))
	fmt.Printf("Total: %d points, %d paths, %d polygons, %d location data points\n",
		len(allPoints), len(allPaths), len(allPolygons), len(allSessionPoints))

	// Find non-colliding filename
	outputPath := findNonCollidingFilename("ble_devices-MERGE", ".kml")
	fmt.Printf("\nWriting merged KML to: %s\n", outputPath)

	// Write merged KML
	if err := writeMergedKML(outputPath, allPoints, allPaths, allPolygons, allSessionPoints); err != nil {
		return fmt.Errorf("failed to write merged KML: %w", err)
	}

	fmt.Printf("✓ Merge complete!\n")
	return nil
}

// extractPlacemarksFromKML parses a KML file and extracts Placemark XML by folder
func extractPlacemarksFromKML(filePath string) ([]string, []string, []string, []GeoLocation, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read file: %w", err)
	}

	text := string(content)

	// Extract placemarks from each folder by name
	points := extractPlacemarksFromFolder(text, "Points")
	paths := extractPlacemarksFromFolder(text, "Paths")
	polygons := extractPlacemarksFromFolder(text, "Polygons")

	// Extract all coordinates for session boundary
	sessionPoints := extractAllCoordinates(text)

	return points, paths, polygons, sessionPoints, nil
}

// extractPlacemarksFromFolder extracts all Placemark elements from a named folder
func extractPlacemarksFromFolder(kmlText, folderName string) []string {
	var placemarks []string

	// Find the folder by name
	folderNameTag := fmt.Sprintf("<name>%s</name>", folderName)
	folderIdx := strings.Index(kmlText, folderNameTag)
	if folderIdx == -1 {
		return placemarks // Folder not found
	}

	// Find the <Folder> tag before the name
	folderStart := strings.LastIndex(kmlText[:folderIdx], "<Folder>")
	if folderStart == -1 {
		return placemarks
	}

	// Find the closing </Folder> tag
	folderEnd := strings.Index(kmlText[folderStart:], "</Folder>")
	if folderEnd == -1 {
		return placemarks
	}
	folderEnd += folderStart

	folderContent := kmlText[folderStart:folderEnd]

	// Extract all <Placemark>...</Placemark> within this folder
	searchStart := 0
	for {
		placemarkStart := strings.Index(folderContent[searchStart:], "<Placemark>")
		if placemarkStart == -1 {
			break
		}
		placemarkStart += searchStart

		placemarkEnd := strings.Index(folderContent[placemarkStart:], "</Placemark>")
		if placemarkEnd == -1 {
			break
		}
		placemarkEnd += placemarkStart + len("</Placemark>")

		placemark := folderContent[placemarkStart:placemarkEnd]
		placemarks = append(placemarks, placemark)

		searchStart = placemarkEnd
	}

	return placemarks
}

// extractAllCoordinates extracts all coordinate data from KML text
func extractAllCoordinates(kmlText string) []GeoLocation {
	var locations []GeoLocation

	coordsStart := "<coordinates>"
	coordsEnd := "</coordinates>"

	searchStart := 0
	for {
		start := strings.Index(kmlText[searchStart:], coordsStart)
		if start == -1 {
			break
		}
		start += searchStart + len(coordsStart)

		end := strings.Index(kmlText[start:], coordsEnd)
		if end == -1 {
			break
		}
		end += start

		coordsText := strings.TrimSpace(kmlText[start:end])

		// Parse coordinate tuples (space-separated)
		tuples := strings.Fields(coordsText)
		for _, tuple := range tuples {
			parts := strings.Split(tuple, ",")
			if len(parts) >= 2 {
				var lon, lat, alt float64
				fmt.Sscanf(parts[0], "%f", &lon)
				fmt.Sscanf(parts[1], "%f", &lat)
				if len(parts) >= 3 {
					fmt.Sscanf(parts[2], "%f", &alt)
				}

				locations = append(locations, GeoLocation{
					Latitude:  lat,
					Longitude: lon,
					Elevation: alt,
				})
			}
		}

		searchStart = end + len(coordsEnd)
	}

	return locations
}

// writeMergedKML writes merged placemarks to a new KML file
func writeMergedKML(outputPath string, points, paths, polygons []string, sessionPoints []GeoLocation) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Write KML header
	file.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	file.WriteString("\n")
	file.WriteString(`<kml xmlns="http://www.opengis.net/kml/2.2">`)
	file.WriteString("\n  <Document>\n")
	file.WriteString(fmt.Sprintf("    <name>BLE Devices - MERGED - %s</name>\n", time.Now().Format("2006-01-02 15:04:05")))

	// Write Points folder
	if len(points) > 0 {
		file.WriteString("    <Folder>\n")
		file.WriteString("      <name>Points</name>\n")
		for _, placemark := range points {
			// Indent the placemark
			indented := strings.ReplaceAll(placemark, "\n", "\n      ")
			file.WriteString("      " + indented + "\n")
		}
		file.WriteString("    </Folder>\n")
	}

	// Write Paths folder
	if len(paths) > 0 {
		file.WriteString("    <Folder>\n")
		file.WriteString("      <name>Paths</name>\n")
		for _, placemark := range paths {
			indented := strings.ReplaceAll(placemark, "\n", "\n      ")
			file.WriteString("      " + indented + "\n")
		}
		file.WriteString("    </Folder>\n")
	}

	// Write Polygons folder
	if len(polygons) > 0 {
		file.WriteString("    <Folder>\n")
		file.WriteString("      <name>Polygons</name>\n")
		for _, placemark := range polygons {
			indented := strings.ReplaceAll(placemark, "\n", "\n      ")
			file.WriteString("      " + indented + "\n")
		}
		file.WriteString("    </Folder>\n")
	}

	// Write Session Boundary folder (recompute from all coordinates)
	if len(sessionPoints) > 0 {
		file.WriteString("    <Folder>\n")
		file.WriteString("      <name>Session Boundary</name>\n")

		// Create session boundary placemark
		hull := computeConvexHull(sessionPoints)
		if len(hull) >= 3 {
			coords := make([]string, len(hull)+1)
			for i, loc := range hull {
				coords[i] = fmt.Sprintf("%.5f,%.5f,%.1f", loc.Longitude, loc.Latitude, loc.Elevation)
			}
			coords[len(hull)] = coords[0] // Close polygon

			description := fmt.Sprintf(
				"&lt;ul&gt;&lt;li&gt;&lt;strong&gt;Total Points:&lt;/strong&gt; %d&lt;/li&gt;&lt;li&gt;&lt;strong&gt;Boundary Points:&lt;/strong&gt; %d&lt;/li&gt;&lt;li&gt;&lt;strong&gt;Merge Time:&lt;/strong&gt; %s&lt;/li&gt;&lt;/ul&gt;",
				len(sessionPoints),
				len(hull),
				time.Now().Format("2006-01-02 15:04:05"),
			)

			file.WriteString("      <Placemark>\n")
			file.WriteString("        <name>Session Area</name>\n")
			file.WriteString(fmt.Sprintf("        <description>%s</description>\n", description))
			file.WriteString("        <Polygon>\n")
			file.WriteString("          <outerBoundaryIs>\n")
			file.WriteString("            <LinearRing>\n")
			file.WriteString(fmt.Sprintf("              <coordinates>%s</coordinates>\n", strings.Join(coords, " ")))
			file.WriteString("            </LinearRing>\n")
			file.WriteString("          </outerBoundaryIs>\n")
			file.WriteString("        </Polygon>\n")
			file.WriteString("      </Placemark>\n")
		}

		file.WriteString("    </Folder>\n")
	}

	// Write KML footer
	file.WriteString("  </Document>\n")
	file.WriteString("</kml>\n")

	return nil
}

// findNonCollidingFilename finds a filename that doesn't exist
// Format: prefix-{i}.ext where i starts at 1 and increments until no collision
func findNonCollidingFilename(prefix, ext string) string {
	// Try without number first
	path := prefix + ext
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	// Try with incrementing counter
	for i := 1; i < 10000; i++ {
		path = fmt.Sprintf("%s-%d%s", prefix, i, ext)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}

	// Fallback (should never happen)
	return fmt.Sprintf("%s-%d%s", prefix, time.Now().Unix(), ext)
}

// createSessionBoundary creates a polygon representing the total session area
// Uses the convex hull of all collected points from all devices
func createSessionBoundary(allPoints []GeoLocation) kml.Element {
	if len(allPoints) < 3 {
		// Need at least 3 points to make a polygon
		return nil
	}

	// Compute convex hull of all points
	hull := computeConvexHull(allPoints)

	if len(hull) < 3 {
		return nil
	}

	// Convert hull to coordinates (and close the polygon)
	coords := make([]kml.Coordinate, len(hull)+1)
	for i, loc := range hull {
		coords[i] = kml.Coordinate{
			Lon: loc.Longitude,
			Lat: loc.Latitude,
			Alt: loc.Elevation,
		}
	}
	// Close the polygon
	coords[len(hull)] = coords[0]

	// Create description with session stats
	description := fmt.Sprintf(
		"<ul><li><strong>Total Points:</strong> %d</li><li><strong>Boundary Points:</strong> %d</li><li><strong>Session Time:</strong> %s</li></ul>",
		len(allPoints),
		len(hull),
		time.Now().Format("2006-01-02 15:04:05"),
	)

	return kml.Placemark(
		kml.Name("Session Area"),
		kml.Description(description),
		kml.Polygon(
			kml.OuterBoundaryIs(
				kml.LinearRing(
					kml.Coordinates(coords...),
				),
			),
		),
	)
}
