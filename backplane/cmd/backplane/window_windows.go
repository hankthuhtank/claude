//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"safisolutions.org/backplane/internal/server"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	dwmapi                = windows.NewLazySystemDLL("dwmapi.dll")
	procGetDpiForSystem   = user32.NewProc("GetDpiForSystem")
	procShowWindow        = user32.NewProc("ShowWindow")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procMessageBoxW       = user32.NewProc("MessageBoxW")
	procDwmSetWindowAttr  = dwmapi.NewProc("DwmSetWindowAttribute")
	procSetProcessDpiCtxt = user32.NewProc("SetProcessDpiAwarenessContext")
)

func hasWindow() bool { return true }

func dpiScale() float64 {
	if procGetDpiForSystem.Find() != nil {
		return 1
	}
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// openWindow shows the UI in a WebView2 window and blocks until it closes.
// It returns false when the WebView2 runtime is unavailable.
func openWindow(srv *server.Server, dataDir string) bool {
	// Per-monitor DPI awareness (also declared in the manifest).
	if procSetProcessDpiCtxt.Find() == nil {
		procSetProcessDpiCtxt.Call(uintptr(^uintptr(3))) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	}
	scale := dpiScale()
	wvData := filepath.Join(os.Getenv("LOCALAPPDATA"), "Backplane", "WebView2")
	if os.Getenv("LOCALAPPDATA") == "" {
		wvData = filepath.Join(dataDir, "WebView2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  wvData,
		WindowOptions: webview2.WindowOptions{
			Title:  "Backplane",
			Width:  uint(1380 * scale),
			Height: uint(880 * scale),
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	darkTitleBar(hwnd)
	w.SetSize(int(1020*scale), int(640*scale), webview2.HintMin)
	srv.OnFocus = func() {
		w.Dispatch(func() {
			procShowWindow.Call(hwnd, 9) // SW_RESTORE
			procSetForeground.Call(hwnd)
		})
	}
	w.Navigate(srv.URL())
	w.Run()
	return true
}

// darkTitleBar matches the window frame to the default dark material.
func darkTitleBar(hwnd uintptr) {
	if procDwmSetWindowAttr.Find() != nil {
		return
	}
	on := int32(1)
	procDwmSetWindowAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&on)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
}

func openURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// waitForQuit keeps the service alive while the UI runs in a browser.
func waitForQuit(url string) {
	msg := "Backplane is open in your web browser because the Microsoft Edge WebView2 Runtime isn't installed.\n\n" +
		"Keep this box open while you work. Choose OK to quit Backplane.\n\n" +
		"Tip: install the WebView2 Runtime from Microsoft to use Backplane in its own window."
	messageBox("Backplane", msg, 0x40) // MB_ICONINFORMATION
}

func messageBox(title, text string, flags uintptr) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
}

func fatal(msg string) {
	messageBox("Backplane", msg, 0x10) // MB_ICONERROR
	os.Exit(1)
}
