package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		return o == "" || strings.HasPrefix(o, "chrome-extension://")
	},
}

// handleWS upgrades and serves the browser extension connection.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade failed: %v", err)
		return
	}
	c := &extConn{ws: ws, alive: true, since: time.Now()}

	s.mu.Lock()
	s.extConns[c] = struct{}{}
	s.mu.Unlock()
	log.Printf("[ws] extension connected (%d total)", len(s.extConns))

	defer func() {
		s.mu.Lock()
		delete(s.extConns, c)
		c.alive = false
		if s.lastUsed == c {
			s.lastUsed = nil
		}
		s.mu.Unlock()
		ws.Close()
		log.Printf("[ws] extension disconnected (%d total)", len(s.extConns))
	}()

	ws.SetReadLimit(8 << 20)
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	type msgAndErr struct {
		msg map[string]any
		err error
	}
	readCh := make(chan msgAndErr, 8)
	go func() {
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				readCh <- msgAndErr{err: err}
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			readCh <- msgAndErr{msg: msg}
		}
	}()

	for {
		select {
		case me := <-readCh:
			if me.err != nil {
				return
			}
			s.onExtMessage(c, me.msg)
		case <-pingTicker.C:
			s.mu.Lock()
			c.sendMu.Lock()
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.sendMu.Unlock()
			s.mu.Unlock()
			if err != nil {
				// Dead connection (browser killed, half-open TCP). Without this
				// the zombie stays in extConns forever and can still be routed
				// to. Drop it; the extension reconnects on its own.
				log.Printf("[ws] ping failed (%v) -> dropping dead connection", err)
				return
			}
		}
	}
}

func (s *Server) onExtMessage(c *extConn, msg map[string]any) {
	// Responses carry an id; match to pending caller.
	if id, ok := msg["id"].(string); ok && id != "" {
		s.mu.Lock()
		if ch, ok := s.pendingReqs[id]; ok {
			delete(s.pendingReqs, id)
			ch <- msg
		}
		s.mu.Unlock()
		return
	}
	switch msg["type"] {
	case "hello":
		c.id, _ = msg["ext_id"].(string)
		c.browser, _ = msg["browser"].(string)
		c.version, _ = msg["version"].(string)
		if caps, ok := msg["caps"].([]any); ok {
			c.caps = map[string]bool{}
			for _, cap := range caps {
				if s2, ok := cap.(string); ok {
					c.caps[s2] = true
				}
			}
		}
		log.Printf("[ws] hello from extension %s (daemon v%s)", c.identity(), Version)
		s.mu.Lock()
		// Multiple browsers stay connected at once (Chrome AND Edge share one
		// unpacked extension id — the browser brand keeps them distinct). If
		// THIS browser's extension reconnects (service worker restart, stale
		// socket not yet reaped), replace the older socket. The freshest
		// hello becomes the default route for hint-less commands.
		for old := range s.extConns {
			if old != c && old.id == c.id && old.browser == c.browser && c.id != "" {
				delete(s.extConns, old)
				old.alive = false
				old.ws.Close()
				log.Printf("[ws] replaced stale connection of %s", c.identity())
			}
		}
		s.lastUsed = c
		s.mu.Unlock()
	case "ping":
		s.sendToExt(c, map[string]any{"type": "pong", "t": time.Now().UnixMilli()})
	}
}

func (s *Server) sendToExt(c *extConn, msg map[string]any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(msg)
}

var pendingMu = &sync.Mutex{}

// callExt runs a command on the RIGHT connected extension and waits for the
// response with the same id. Routing ("who asked,谁的 browser 处理"):
//   - args.ext_id pins an exact browser (the side panel sends its own id, so a
//     move requested from Edge's panel moves Edge's page);
//   - otherwise every capable browser is tried in order — most recently used
//     first — and a "no such tab here" answer falls through to the next one,
//     so url/tab_id hints resolve against the browser that actually has the
//     tab.
func (s *Server) callExt(cmd string, args map[string]any, timeout time.Duration) (map[string]any, error) {
	candidates := s.routeOrder(cmd, args)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no extension connected")
	}
	var lastErr error
	for i, c := range candidates {
		resp, err := s.callOne(c, cmd, args, timeout)
		if err != nil {
			// Send failure / timeout: the op may have half-run there — do not
			// blind-retry the same tab elsewhere; only a clean "not my tab"
			// answer may fall through.
			lastErr = err
			continue
		}
		if ok, _ := resp["ok"].(bool); !ok {
			msg, _ := resp["error"].(string)
			if tabMiss(msg) && i < len(candidates)-1 {
				lastErr = fmt.Errorf("%s", msg)
				continue
			}
		}
		s.mu.Lock()
		s.lastUsed = c
		s.mu.Unlock()
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no extension connected")
}

func (s *Server) callOne(c *extConn, cmd string, args map[string]any, timeout time.Duration) (map[string]any, error) {
	id := fmt.Sprintf("r%d", time.Now().UnixNano())
	ch := make(chan map[string]any, 1)
	pendingMu.Lock()
	s.pendingReqs[id] = ch
	pendingMu.Unlock()
	defer func() {
		pendingMu.Lock()
		delete(s.pendingReqs, id)
		pendingMu.Unlock()
	}()

	payload := map[string]any{"id": id, "cmd": cmd, "args": args}
	if err := s.sendToExt(c, payload); err != nil {
		return nil, fmt.Errorf("send to extension: %v", err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("extension timed out after %v on cmd %q", timeout, cmd)
	}
}

// tabMiss marks responses that mean "this browser cannot serve that tab" and
// the command should try the next connected browser instead.
var tabMissPatterns = []string{
	"no open tab matching url",
	"no active tab found",
	"no longer exists",
	"cannot locate on browser-internal page",
	"cannot measure browser-internal page",
	"no window (do a locate first)",
	"content script not reachable on this page",
}

func tabMiss(msg string) bool {
	for _, p := range tabMissPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// routeOrder returns the candidate connections for cmd, best first: an
// "id@browser" hint pins one exact browser, a plain id matches every browser
// carrying that extension (most recently used first), then the most recently
// used / newest hellos.
func (s *Server) routeOrder(cmd string, args map[string]any) []*extConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	capable := func(c *extConn) bool { return c.alive && (c.caps == nil || c.caps[cmd]) }
	if hint, _ := args["ext_id"].(string); hint != "" {
		if strings.Contains(hint, "@") {
			for c := range s.extConns {
				if c.identity() == hint && capable(c) {
					return []*extConn{c}
				}
			}
		} else {
			var m []*extConn
			for c := range s.extConns {
				if c.id == hint && capable(c) {
					m = append(m, c)
				}
			}
			if len(m) > 0 {
				sort.Slice(m, func(i, j int) bool {
					if (m[i] == s.lastUsed) != (m[j] == s.lastUsed) {
						return m[i] == s.lastUsed
					}
					return m[i].since.After(m[j].since)
				})
				return m
			}
		}
		// Hinted browser not connected (closed?): fall back to normal order.
	}
	var list []*extConn
	for c := range s.extConns {
		if capable(c) {
			list = append(list, c)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if (list[i] == s.lastUsed) != (list[j] == s.lastUsed) {
			return list[i] == s.lastUsed
		}
		return list[i].since.After(list[j].since)
	})
	return list
}

func (s *Server) extensionConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.extConns) > 0
}

func (s *Server) extensionVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := ""
	for c := range s.extConns {
		if latest == "" || cmpVersion(c.version, latest) > 0 {
			latest = c.version
		}
	}
	return latest
}

// extensionsInfo describes every connected browser for /status.
func (s *Server) extensionsInfo() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for c := range s.extConns {
		caps := []string{}
		for k := range c.caps {
			caps = append(caps, k)
		}
		sort.Strings(caps)
		out = append(out, map[string]any{
			"ext_id":      c.identity(),
			"browser":     c.browser,
			"version":     c.version,
			"caps":        caps,
			"connected_s": int(time.Since(c.since).Seconds()),
			"default":     c == s.lastUsed,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["ext_id"].(string)
		b, _ := out[j]["ext_id"].(string)
		return a < b
	})
	return out
}

// cmpVersion compares dotted numeric versions: -1 / 0 / 1.
func cmpVersion(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return 0
}

func pidSelf() int { return os.Getpid() }

func exitProcess(code int) { os.Exit(code) }
