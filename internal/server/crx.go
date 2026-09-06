package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CrxDir holds mouse-bridge.crx + updates.xml served to Chrome's
// ExtensionInstallForcelist policy installer.
func crxDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "extension"
	}
	return filepath.Join(home, ".mouse-bridge", "extension")
}

const updatesXML = `<?xml version="1.0" encoding="UTF-8"?>
<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">
  <app appid="ehjajiiiapinmkkafcfnoconecchmoae">
    <updatecheck codebase="http://127.0.0.1:10087/crx/mouse-bridge.crx" version="1.0.0" />
  </app>
</gupdate>
`

func (s *Server) handleCrxManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	http.ServeContent(w, r, "updates.xml", time.Now(), strings.NewReader(updatesXML))
}

func (s *Server) handleCrxFile(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(crxDir(), "mouse-bridge.crx")
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "crx not deployed", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-chrome-extension")
	http.ServeFile(w, r, p)
}
