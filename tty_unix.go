// tty_unix.go holds the terminal helpers for --approve on Unix systems: throwing away input
// typed ahead of a question, and checking that yonder is not running in the background.

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// flushInput discards anything already typed into the terminal behind in but not yet read, so
// that a stray key cannot answer a question the owner has not seen. It does nothing when in is
// not a terminal.
func flushInput(in io.Reader) {
	if f, ok := in.(*os.File); ok {
		discardTerminalInput(int(f.Fd()))
	}
}

// inForeground reports whether yonder owns the terminal behind f. A process in the background
// that reads from its terminal is stopped by the shell, which would freeze the server too. If
// the question cannot be answered (an error), it assumes yes.
func inForeground(f *os.File) bool {
	group, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
	return err != nil || group == unix.Getpgrp()
}
