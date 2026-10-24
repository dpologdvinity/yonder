// tty_bsd.go throws away a macOS or BSD terminal's unread input.

//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "golang.org/x/sys/unix"

// fread is the flag that tells TIOCFLUSH to empty the input queue (FREAD in <sys/fcntl.h>).
const fread = 1

// discardTerminalInput empties the input queue of the terminal with this file descriptor.
func discardTerminalInput(fd int) {
	_ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread) // best effort: if it fails, the question is still asked
}
