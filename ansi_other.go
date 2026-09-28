//go:build !windows

package main

import (
	"os"
)

// Non-Windows builds are a development convenience; emit ANSI styling only
// when stdout is a terminal that has not opted out — piped output should stay
// clean text.
func enableANSI() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
