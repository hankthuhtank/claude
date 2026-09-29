//go:build !windows

package app

import (
	"fmt"
	"os/exec"
	"runtime"
)

// AppUserModelID identifies Backplane's notifications (Windows only).
const AppUserModelID = "SafiSolutions.Backplane"

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openBrowser(url string) error { return exec.Command(opener(), url).Start() }

func openPath(dir string) error { return exec.Command(opener(), dir).Start() }

func setBackgroundTask(enabled bool) error {
	if enabled {
		return fmt.Errorf("background checks while Backplane is closed are available on Windows only")
	}
	return nil
}

// RegisterNotifications is a no-op outside Windows.
func RegisterNotifications(iconPath string) error { return nil }

// DesktopNotify uses notify-send where available.
func DesktopNotify(title, body string) {
	if p, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(p, title, body).Start()
	}
}

// BackgroundTaskSupported reports whether scheduled background checks exist
// on this platform.
const BackgroundTaskSupported = false
