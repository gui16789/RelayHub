//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
)

// fatalDialog reports an unrecoverable startup error and exits.
//
// Non-Windows desktop builds are launched from a terminal often enough that
// stderr is the dependable channel; wiring up Cocoa/GTK dialogs here would
// drag a CGO dependency into a path that only ever runs once, at startup,
// before the Wails runtime exists.
func fatalDialog(title, message string) {
	log.Printf("%s: %s", title, message)
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, message)
	os.Exit(1)
}
