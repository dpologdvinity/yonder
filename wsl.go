// wsl.go detects Windows Subsystem for Linux in its default NAT mode, where other devices
// cannot reach the machine, and explains how to fix it.

package main

import (
	"os"
	"os/exec"
	"strings"
)

// natHint is the message printed when WSL runs in NAT mode and other devices cannot connect.
const natHint = "WSL is in NAT mode, so other devices cannot reach it.\n" +
	"Fix: set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig, then run `wsl --shutdown`.\n" +
	"Or run the Windows build, yonder.exe.\n"

// wslHint returns natHint when running on WSL in NAT networking mode.
func wslHint(osrelease, mode string) string {
	if strings.Contains(strings.ToLower(osrelease), "microsoft") && strings.TrimSpace(mode) == "nat" {
		return natHint
	}
	return ""
}

// detectWSLHint checks the real system. Any failure means no hint.
func detectWSLHint() string {
	rel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil || !strings.Contains(strings.ToLower(string(rel)), "microsoft") {
		return ""
	}
	mode, err := exec.Command("wslinfo", "--networking-mode").Output()
	if err != nil {
		return ""
	}
	return wslHint(string(rel), string(mode))
}
