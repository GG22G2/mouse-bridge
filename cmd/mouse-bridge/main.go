// mouse-bridge daemon: exposes real OS-level mouse control (human-like
// trajectories) to AI agents over HTTP, driven through a Chrome extension
// for element location. Mirrors the kimi-webbridge daemon layout.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"mousebridge/internal/server"
	"mousebridge/internal/win"
)

const (
	listenAddr = "127.0.0.1:10087"
	appDirName = ".mouse-bridge"
)

func homeAppDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, appDirName)
}

func setupLogging() (*os.File, error) {
	dir := filepath.Join(homeAppDir(), "logs")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.SetFlags(log.Ldate | log.Ltime)
	return f, nil
}

func writePid() {
	os.MkdirAll(homeAppDir(), 0o755)
	os.WriteFile(filepath.Join(homeAppDir(), "daemon.pid"), []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
}

func removePid() { os.Remove(filepath.Join(homeAppDir(), "daemon.pid")) }

func daemonAlive() bool {
	resp, err := http.Get("http://" + listenAddr + "/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func startDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008,
	}
	return cmd.Start()
}

func stopDaemon() {
	resp, err := http.Post("http://"+listenAddr+"/shutdown", "application/json", nil)
	if err == nil {
		resp.Body.Close()
		time.Sleep(400 * time.Millisecond)
	}
	// Fallback: kill via pid file.
	b, err := os.ReadFile(filepath.Join(homeAppDir(), "daemon.pid"))
	if err == nil {
		var pid int
		if n, _ := fmt.Sscanf(string(b), "%d", &pid); n == 1 && pid > 0 {
			exec.Command("taskkill", "/F", "/PID", fmt.Sprint(pid)).Run()
		}
	}
	removePid()
}

func main() {
	win.EnablePerMonitorDpiV2()
	win.TimeBeginPeriod()

	mode := "start"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	switch mode {
	case "start":
		if daemonAlive() {
			fmt.Println("mouse-bridge already running on " + listenAddr)
			return
		}
		if err := startDetached(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
			os.Exit(1)
		}
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if daemonAlive() {
				fmt.Println("mouse-bridge daemon started on " + listenAddr)
				return
			}
		}
		fmt.Fprintln(os.Stderr, "daemon did not come up in time; check logs")
		os.Exit(1)
	case "run":
		f, err := setupLogging()
		if err != nil {
			fmt.Fprintln(os.Stderr, "logging setup failed:", err)
			os.Exit(1)
		}
		defer f.Close()
		writePid()
		defer removePid()
		log.Printf("[main] mouse-bridge daemon v%s starting (pid %d)", server.Version, os.Getpid())
		s := server.New()
		if err := s.Run(listenAddr); err != nil {
			log.Printf("[main] server exited: %v", err)
			os.Exit(1)
		}
	case "stop":
		stopDaemon()
		fmt.Println("stopped")
	case "status":
		if !daemonAlive() {
			fmt.Println("not running")
			os.Exit(1)
		}
		resp, _ := http.Get("http://" + listenAddr + "/status")
		defer resp.Body.Close()
		io.Copy(os.Stdout, resp.Body)
		fmt.Println()
	case "restart":
		stopDaemon()
		time.Sleep(300 * time.Millisecond)
		startDetached()
		fmt.Println("restarted")
	case "version":
		fmt.Println("mouse-bridge", server.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: mouse-bridge [start|run|stop|status|restart|version]\n")
		os.Exit(2)
	}
}
