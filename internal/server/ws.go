package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	c := &extConn{ws: ws, alive: true}

	s.mu.Lock()
	s.extConns[c] = struct{}{}
	s.mu.Unlock()
	log.Printf("[ws] extension connected (%d total)", len(s.extConns))

	defer func() {
		s.mu.Lock()
		delete(s.extConns, c)
		if s.extLatest == c {
			s.extLatest = nil
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
		c.version, _ = msg["version"].(string)
		if caps, ok := msg["caps"].([]any); ok {
			c.caps = map[string]bool{}
			for _, cap := range caps {
				if s2, ok := cap.(string); ok {
					c.caps[s2] = true
				}
			}
		}
		log.Printf("[ws] hello from extension %s v%s caps=%v (daemon v%s)", c.id, c.version, c.caps, Version)
		s.mu.Lock()
		// Route to the freshest code: a hello with a version >= the current
		// extLatest's takes over. Mixed-version windows (extension just
		// reloaded in one browser while another still runs the old one)
		// therefore converge on the newest instance.
		if s.extLatest == nil || cmpVersion(c.version, s.extLatest.version) >= 0 {
			s.extLatest = c
		}
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

// callExt sends a command to the latest connected extension and waits for
// the response with the same id.
func (s *Server) callExt(cmd string, args map[string]any, timeout time.Duration) (map[string]any, error) {
	s.mu.Lock()
	c := s.pickConn(cmd)
	if c == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("no extension connected")
	}
	s.mu.Unlock()

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

func (s *Server) extensionConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.extLatest != nil
}

// pickConn routes cmd to a connection that declared the capability. A conn
// with caps==nil predates capability declarations (or is mid-handshake) and
// is trusted by default; a conn WITH caps that lacks the cmd runs stale code
// and must be routed around when any other conn declared it.
func (s *Server) pickConn(cmd string) *extConn {
	if s.extLatest != nil && (s.extLatest.caps == nil || s.extLatest.caps[cmd]) {
		return s.extLatest
	}
	for c := range s.extConns {
		if c != s.extLatest && c.caps != nil && c.caps[cmd] {
			log.Printf("[ws] extLatest lacks cap %q -> routing to another capable connection", cmd)
			return c
		}
	}
	return s.extLatest
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

func (s *Server) extensionVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.extLatest == nil {
		return ""
	}
	return s.extLatest.version
}

func pidSelf() int { return os.Getpid() }

func exitProcess(code int) { os.Exit(code) }
