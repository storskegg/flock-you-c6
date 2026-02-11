package main

import (
	"time"

	"github.com/gen2brain/beeep"
)

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
