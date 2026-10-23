package main

import "testing"

const wantNATHint = "WSL is in NAT mode, so other devices cannot reach it.\n" +
	"Fix: set networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig, then run `wsl --shutdown`.\n" +
	"Or run the Windows build, yonder.exe.\n"

func TestWSLHint(t *testing.T) {
	tests := []struct {
		name, osrelease, mode string
		wantHint              bool
	}{
		{"wsl nat", "6.18.54.1-microsoft-standard-WSL2\n", "nat\n", true},
		{"wsl mirrored", "6.18.54.1-microsoft-standard-WSL2\n", "mirrored\n", false},
		{"plain linux", "6.8.0-45-generic\n", "nat\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wslHint(tt.osrelease, tt.mode)
			if tt.wantHint && got != wantNATHint {
				t.Errorf("got %q, want the NAT hint", got)
			}
			if !tt.wantHint && got != "" {
				t.Errorf("got %q, want no hint", got)
			}
		})
	}
}
