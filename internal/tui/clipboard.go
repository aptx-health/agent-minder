package tui

import (
	"os/exec"
	"runtime"
	"strings"
)

// copyToClipboard writes text to the system clipboard.
// Supports macOS (pbcopy), Linux/WSL (xclip, xsel, wl-copy), and Windows (clip.exe via WSL).
func copyToClipboard(text string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		// Try clipboard commands in preference order:
		// wl-copy (Wayland), xclip (X11), xsel (X11), clip.exe (WSL).
		for _, candidate := range []struct {
			name string
			args []string
		}{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
			{"xsel", []string{"--clipboard", "--input"}},
			{"clip.exe", nil}, // WSL
		} {
			if path, err := exec.LookPath(candidate.name); err == nil {
				cmd = exec.Command(path, candidate.args...)
				break
			}
		}
	case "windows":
		cmd = exec.Command("clip.exe")
	}

	if cmd == nil {
		return exec.ErrNotFound
	}

	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
