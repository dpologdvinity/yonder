package main

import (
	"errors"
	"syscall"
)

// wsaeaddrinuse is WSAEADDRINUSE; syscall.EADDRINUSE does not match it on Windows.
const wsaeaddrinuse = syscall.Errno(10048)

func isAddrInUse(err error) bool { return errors.Is(err, wsaeaddrinuse) }
