package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	s.extLatest = c
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
			_ = c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.sendMu.Unlock()
			s.mu.Unlock()
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
		log.Printf("[ws] hello from extension %s v%s (daemon v%s)", c.id, c.version, Version)
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
	c := s.extLatest
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
