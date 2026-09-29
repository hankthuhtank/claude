//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"safisolutions.org/backplane/internal/server"
)

// hasWindow is false: outside Windows the service runs headless (--serve).
func hasWindow() bool { return false }

func openWindow(srv *server.Server, dataDir string) bool { return false }

func openURL(u string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, u).Start()
}

func waitForQuit(url string) { select {} }

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
