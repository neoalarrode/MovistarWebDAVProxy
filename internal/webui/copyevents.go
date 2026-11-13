package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleCopyEvents envía el estado de la copia en cuanto cambia (Server-Sent
// Events), para que la tarjeta se actualice en tiempo real sin consultar cada X
// segundos. Vigila cada 250 ms las fechas y tamaños de los archivos de los que sale
// el estado; si nada cambia, manda un latido cada 15 s.
func (s *Server) handleCopyEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(payload string) bool {
		// El panel tiene WriteTimeout de 30 s: se alarga en cada envío. Si no se
		// puede, el flujo se corta y el navegador se reconecta solo.
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := fmt.Fprint(w, payload); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("retry: 1000\n\n") {
		return
	}

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var lastSig string
	lastSent := time.Time{}
	for {
		sig := s.copySignature()
		if sig != lastSig || time.Since(lastSent) > 15*time.Second {
			if sig != lastSig {
				b, err := json.Marshal(readCopyStatusWith(s.copyDir, s.copyControlDir, s.reads))
				if err == nil && !send("data: "+string(b)+"\n\n") {
					return
				}
			} else if !send(": latido\n\n") {
				return
			}
			lastSig, lastSent = sig, time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// copySignature resume la fecha y el tamaño de los archivos de estado de la copia:
// si cambia, hay algo nuevo que enviar.
func (s *Server) copySignature() string {
	if s.copyDir == "" {
		return ""
	}
	var b strings.Builder
	stamp := func(path string) {
		if fi, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%d:%d;", fi.ModTime().UnixNano(), fi.Size())
		} else {
			b.WriteString("-;")
		}
	}
	files, _ := filepath.Glob(filepath.Join(s.copyDir, "*"))
	for _, f := range files {
		stamp(f)
	}
	if s.copyControlDir != "" {
		stamp(filepath.Join(s.copyControlDir, "bwlimit"))
	}
	// Las lecturas "en búfer" envejecen aunque no cambie ningún archivo.
	fmt.Fprintf(&b, "%d", time.Now().Unix()/5)
	return b.String()
}
