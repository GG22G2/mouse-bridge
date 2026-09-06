// mouse-bridge native messaging host: lets the browser extension wake the
// daemon. The browser spawns THIS process when the extension calls
// chrome.runtime.connectNative('com.mousebridge.daemon'); this process then
// makes sure the real daemon (mouse-bridge.exe, same directory) is running.
//
// Protocol: Chrome native messaging — 4-byte little-endian length prefix +
// UTF-8 JSON on stdio. Commands:
//
//	{"cmd":"wake"} ->
//	  daemon already alive?      {"ok":true,"already_running":true}
//	  spawned it just now:       {"ok":true,"spawned":true}
//	  failed:                    {"ok":false,"error":"..."}
//
// Duplicate-launch safety: the /status check avoids most double spawns, and
// the port bind itself is the single-instance lock — a second daemon loses
// the bind on 127.0.0.1:10087 and exits immediately. Stdin EOF (browser
// closed / port disconnected) ends this process; the detached daemon lives on.
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const statusURL = "http://127.0.0.1:10087/status"

func main() {
	logf("native host started (pid %d)", os.Getpid())
	for {
		raw, err := readMessage(os.Stdin)
		if err != nil {
			logf("stdin closed (%v) -> exit", err)
			return
		}
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		cmd, _ := req["cmd"].(string)

		var out map[string]any
		switch cmd {
		case "wake":
			out = wake()
		default:
			out = map[string]any{"ok": false, "error": "unknown cmd: " + cmd}
		}
		logf("cmd=%q -> %v", cmd, out)
		if err := writeMessage(os.Stdout, out); err != nil {
			logf("write failed (%v) -> exit", err)
			return
		}
	}
}

// wake brings the daemon up exactly once and reports what happened.
func wake() map[string]any {
	if daemonAlive() {
		return map[string]any{"ok": true, "already_running": true}
	}
	if err := spawnDaemon(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "spawned": true}
}

func daemonAlive() bool {
	c := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(statusURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == 200
}

// spawnDaemon launches the sibling mouse-bridge.exe detached and waits until
// it answers on 10087 (or fails fast if another instance won the race and the
// port is still somehow not answering).
func spawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	daemon := filepath.Join(filepath.Dir(exe), "mouse-bridge.exe")
	if _, err := os.Stat(daemon); err != nil {
		return fmt.Errorf("daemon exe not found next to native host (%s): %v", daemon, err)
	}
	cmd := exec.Command(daemon, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release() // the daemon must outlive this host process

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if daemonAlive() {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not come up within 8s (check ~/.mouse-bridge/logs/daemon.log)")
}

func readMessage(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.LittleEndian.Uint32(lenBuf[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(b)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func logf(format string, args ...any) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logPath := filepath.Join(dir, ".mouse-bridge", "logs")
	os.MkdirAll(logPath, 0o755)
	f, err := os.OpenFile(filepath.Join(logPath, "nativehost.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006/01/02 15:04:05"), fmt.Sprintf(format, args...))
}
