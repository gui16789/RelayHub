//go:build windows

package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows"
)

// fatalDialog reports an unrecoverable startup error and exits.
//
// The desktop build runs with no console attached (-H windowsgui), so a
// native message box is the only way the user ever sees why the app refused
// to start.
func fatalDialog(title, message string) {
	log.Printf("%s: %s", title, message)
	_, _ = windows.MessageBox(
		0,
		windows.StringToUTF16Ptr(message),
		windows.StringToUTF16Ptr(title),
		windows.MB_OK|windows.MB_ICONERROR,
	)
	os.Exit(1)
}
