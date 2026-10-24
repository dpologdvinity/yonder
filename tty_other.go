// tty_other.go provides do-nothing terminal helpers for systems without the Unix calls,
// such as Windows, where --approve still works but typed-ahead input is not discarded.

//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import (
	"io"
	"os"
)

// flushInput does nothing on this system.
func flushInput(io.Reader) {}

// inForeground always reports true on this system.
func inForeground(*os.File) bool { return true }
