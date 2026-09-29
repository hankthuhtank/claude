// Command backplane is the Backplane desktop app: a local service plus a
// WebView2 window. Flags:
//
//	--check-all   run one quick check of every monitored backend and exit
//	              (used by the optional Windows background task)
//	--serve       run the service only and print the URL (development, QA)
//	--browser     open the UI in the default browser instead of a window
//	--data DIR    use a different data folder
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/app"
	"safisolutions.org/backplane/internal/server"
	"safisolutions.org/backplane/internal/store"
	"safisolutions.org/backplane/internal/uiassets"
)

// version is set at build time (-ldflags "-X main.version=…").
var version = "1.0.0"

type instance struct {
	Port  string `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func main() {
	checkAll := flag.Bool("check-all", false, "run a quick check of every monitored backend and exit")
	serve := flag.Bool("serve", false, "run the local service without a window and print its URL")
	browser := flag.Bool("browser", false, "open the UI in the default browser")
	dataDir := flag.String("data", "", "data folder (default: per-user app data)")
	flag.Parse()
	app.Version = version

	dir := *dataDir
	if dir == "" {
		dir = store.DefaultDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatal("Backplane could not create its data folder:\n" + err.Error())
	}
	instPath := filepath.Join(dir, "instance.json")

	if *checkAll {
		if alive(instPath, false) {
			return // the open window is already monitoring
		}
		a, err := app.New(app.Options{DataDir: dir, Notify: app.DesktopNotify})
		if err != nil {
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		a.CheckAll(ctx)
		a.Close()
		return
	}

	// A second launch brings the existing window forward instead.
	if !*serve && alive(instPath, true) {
		return
	}

	a, err := app.New(app.Options{DataDir: dir, Notify: app.DesktopNotify})
	if err != nil {
		fatal("Backplane could not open its data:\n" + err.Error())
	}
	defer a.Close()
	if exe, err := os.Executable(); err == nil {
		_ = app.RegisterNotifications(filepath.Join(filepath.Dir(exe), "Backplane.png"))
	}
	if !*serve {
		go a.SyncBackgroundTask()
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	a.StartMonitor(ctx)

	srv := server.New(a, uiassets.FS())
	if err := srv.Start(); err != nil {
		fatal("Backplane could not start its local service:\n" + err.Error())
	}
	defer srv.Close()
	writeInstance(instPath, instance{Port: srv.Port(), Token: srv.Token, PID: os.Getpid()})
	defer os.Remove(instPath)

	if *serve || !hasWindow() {
		fmt.Println(srv.URL())
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		return
	}
	if *browser || !openWindow(srv, dir) {
		// No WebView2 runtime (or asked for the browser): run in the
		// default browser and keep going until the user quits.
		_ = openURL(srv.URL())
		waitForQuit(srv.URL())
	}
}

func writeInstance(path string, in instance) {
	bs, _ := json.Marshal(in)
	_ = os.WriteFile(path, bs, 0o600)
}

// alive reports whether another Backplane window is running; with focus it
// also asks that window to come to the front.
func alive(path string, focus bool) bool {
	bs, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var in instance
	if json.Unmarshal(bs, &in) != nil || in.Port == "" {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	endpoint := "/api/focus"
	if !focus {
		endpoint = "/api/call/SystemInfo"
	}
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+in.Port+endpoint, strings.NewReader("{}"))
	req.Header.Set("X-Backplane-Token", in.Token)
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
