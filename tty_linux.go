// tty_linux.go throws away a Linux terminal's unread input.

//go:build linux

package main

import "golang.org/x/sys/unix"

// discardTerminalInput empties the input queue of the terminal with this file descriptor.
func discardTerminalInput(fd int) {
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) // best effort: if it fails, the question is still asked
}
