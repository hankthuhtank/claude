//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// AppUserModelID identifies Backplane's notifications to Windows.
const AppUserModelID = "SafiSolutions.Backplane"

const taskName = "Backplane Monitor"

func hidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func shellOpen(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func openBrowser(url string) error { return shellOpen(url) }

func openPath(dir string) error { return shellOpen(dir) }

// setBackgroundTask creates or removes a per-user scheduled task that runs a
// quick check of every monitored project each hour while Backplane is closed.
// No administrator rights are needed for a task that runs as the user.
func setBackgroundTask(enabled bool) error {
	if !enabled {
		cmd := exec.Command("schtasks", "/Delete", "/TN", taskName, "/F")
		hidden(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "cannot find") {
			return fmt.Errorf("could not remove the background task: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("schtasks", "/Create", "/TN", taskName, "/TR", `"`+exe+`" --check-all`, "/SC", "MINUTE", "/MO", "60", "/F")
	hidden(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not create the background task: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RegisterNotifications registers Backplane's name and icon for toast
// notifications (per user, no administrator rights).
func RegisterNotifications(iconPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+AppUserModelID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("DisplayName", "Backplane"); err != nil {
		return err
	}
	if iconPath != "" {
		if abs, err := filepath.Abs(iconPath); err == nil {
			_ = k.SetStringValue("IconUri", abs)
		}
	}
	return nil
}

var toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml($env:BP_TOAST)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:BP_AUMID).Show([Windows.UI.Notifications.ToastNotification]::new($x))`

// DesktopNotify shows a Windows toast notification. Text is passed through
// environment variables, never interpolated into the script.
func DesktopNotify(title, body string) {
	xml := `<toast><visual><binding template="ToastGeneric"><text>` + xmlEscape(title) + `</text><text>` + xmlEscape(body) + `</text></binding></visual></toast>`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "BP_TOAST="+xml, "BP_AUMID="+AppUserModelID)
	hidden(cmd)
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	if len(s) > 240 {
		s = s[:237] + "…"
	}
	return r.Replace(s)
}

// BackgroundTaskSupported reports whether scheduled background checks exist
// on this platform.
const BackgroundTaskSupported = true
