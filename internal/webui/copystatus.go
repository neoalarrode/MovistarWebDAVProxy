package webui

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Estado de la copia masiva a la nube que hace un script externo con rclone.
// El proxy no la controla: solo lee los ficheros que deja el script en
// COPY_STATUS_DIR (montado en solo lectura) y escribe órdenes en COPY_CONTROL_DIR:
//
//	plan.txt       bibliotecas en orden, una por línea
//	progress.log   "<fecha> START <biblioteca>" y "<fecha> END <biblioteca> rc=<código>"
//	<biblioteca>.log  log de rclone con --stats-one-line
//	STOP           motivo de una parada automática (si existe)
//	control/bwlimit   límite de ancho de banda pedido desde el panel ("off", "20M"...)

type copyLibrary struct {
	Name   string `json:"name"`
	State  string `json:"state"` // pending | running | done | failed
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
	Result string `json:"rc,omitempty"`
}

type copyStatus struct {
	Enabled      bool          `json:"enabled"`
	Libraries    []copyLibrary `json:"libraries"`
	Current      string        `json:"current,omitempty"`
	Transferred  string        `json:"transferred,omitempty"`
	Total        string        `json:"total,omitempty"`
	Percent      string        `json:"percent,omitempty"`
	Speed        string        `json:"speed,omitempty"`
	ETA          string        `json:"eta,omitempty"`
	StatsAt      string        `json:"stats_at,omitempty"`
	RecentErrors []string      `json:"recent_errors"`
	ErrorCount   int           `json:"error_count"`
	Stop         string        `json:"stop,omitempty"`
	BWLimit      string        `json:"bwlimit"`
}

var (
	// "2026-11-12T22:43:38+01:00 START Peliculas" o "2026-11-12 22:43:38 INICIO Peliculas";
	// fin: "... END|FIN Peliculas rc=0" (el rc puede ir entre paréntesis).
	progressLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}(?:[ T]\S+)?)\s+(START|END|INICIO|FIN)\s+(.+?)(?:\s+\(?rc=(-?\d+)\)?)?\s*$`)
	// Línea de --stats-one-line: "2026/11/12 22:41:00 INFO  :  1.2 GiB / 1.3 TiB, 0%, 25.1 MiB/s, ETA 15h53m"
	statsLine = regexp.MustCompile(`^(\S+ \S+) INFO\s+:\s+([\d.]+\s*\S*B) / ([\d.]+\s*\S*B), (\d+|-)%, ([\d.]+\s*\S*B/s), ETA (\S+)`)
	bwlimitRe = regexp.MustCompile(`^(off|\d+(\.\d+)?[KMG]?)$`)
)

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// tailLines devuelve las líneas de los últimos max bytes del fichero.
func tailLines(path string, max int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cut := false
	if st, err := f.Stat(); err == nil && st.Size() > max {
		_, _ = f.Seek(st.Size()-max, io.SeekStart)
		cut = true
	}
	b, _ := io.ReadAll(f)
	lines := strings.Split(string(b), "\n")
	if cut && len(lines) > 0 {
		lines = lines[1:] // la primera está cortada
	}
	return lines
}

func readCopyStatus(dir, controlDir string) copyStatus {
	st := copyStatus{RecentErrors: []string{}, BWLimit: "off"}
	if dir == "" {
		return st
	}
	if _, err := os.Stat(dir); err != nil {
		return st
	}
	st.Enabled = true

	libs := map[string]*copyLibrary{}
	var order []string
	add := func(name string) *copyLibrary {
		if l, ok := libs[name]; ok {
			return l
		}
		libs[name] = &copyLibrary{Name: name, State: "pending"}
		order = append(order, name)
		return libs[name]
	}
	for _, name := range readLines(filepath.Join(dir, "plan.txt")) {
		add(name)
	}
	for _, line := range readLines(filepath.Join(dir, "progress.log")) {
		m := progressLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		l := add(m[3])
		if m[2] == "START" || m[2] == "INICIO" {
			l.State, l.Start, l.End, l.Result = "running", m[1], "", ""
			st.Current = l.Name
		} else {
			l.End, l.Result = m[1], m[4]
			if m[4] == "0" {
				l.State = "done"
			} else {
				l.State = "failed"
			}
			if st.Current == l.Name {
				st.Current = ""
			}
		}
	}
	for _, name := range order {
		st.Libraries = append(st.Libraries, *libs[name])
	}

	// Estadísticas de la biblioteca en curso (o de la última que corrió).
	logLib := st.Current
	if logLib == "" {
		for i := len(st.Libraries) - 1; i >= 0; i-- {
			if st.Libraries[i].State != "pending" {
				logLib = st.Libraries[i].Name
				break
			}
		}
	}
	if logLib != "" {
		for _, line := range tailLines(filepath.Join(dir, logLib+".log"), 256<<10) {
			if m := statsLine.FindStringSubmatch(line); m != nil {
				st.StatsAt, st.Transferred, st.Total, st.Percent, st.Speed, st.ETA = m[1], m[2], m[3], m[4], m[5], m[6]
			}
			if strings.Contains(line, " ERROR ") {
				st.ErrorCount++
				st.RecentErrors = append(st.RecentErrors, line)
			}
		}
		if n := len(st.RecentErrors); n > 5 {
			st.RecentErrors = st.RecentErrors[n-5:]
		}
	}

	if b, err := os.ReadFile(filepath.Join(dir, "STOP")); err == nil {
		st.Stop = strings.TrimSpace(string(b))
		if st.Stop == "" {
			st.Stop = "parada automática (sin motivo registrado)"
		}
	}
	if controlDir != "" {
		if b, err := os.ReadFile(filepath.Join(controlDir, "bwlimit")); err == nil && strings.TrimSpace(string(b)) != "" {
			st.BWLimit = strings.TrimSpace(string(b))
		}
	}
	return st
}

func (s *Server) handleCopyStatus(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, readCopyStatus(s.copyDir, s.copyControlDir))
}

// handleCopyBWLimit guarda el límite pedido; el script de copia lo aplica en caliente.
func (s *Server) handleCopyBWLimit(w http.ResponseWriter, r *http.Request) {
	if s.copyControlDir == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": "COPY_CONTROL_DIR no configurado"})
		return
	}
	var req struct {
		Rate string `json:"rate"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	rate := strings.TrimSpace(req.Rate)
	if !bwlimitRe.MatchString(rate) {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": "Límite no válido (usa off, 20M, 500K...)"})
		return
	}
	if err := os.WriteFile(filepath.Join(s.copyControlDir, "bwlimit"), []byte(rate+"\n"), 0o644); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "bwlimit": rate})
}

func (s *Server) handleWebSession(w http.ResponseWriter, r *http.Request) {
	if s.webStatus == nil {
		jsonResponse(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	out := s.webStatus()
	out["enabled"] = true
	jsonResponse(w, http.StatusOK, out)
}
