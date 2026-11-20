package webdav

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"movistar-webdav-proxy/internal/notifier"
)

// ClientCopy es el estado que un cliente (p. ej. «Enviar a > Nube» en Windows) manda
// de su copia con rclone: el proxy solo ve lo que le llega, el cliente sabe el total.
type ClientCopy struct {
	ID         string    `json:"id"`
	Dest       string    `json:"dest"`        // carpeta de destino en claro, p. ej. «Netflix/Serie»
	Bytes      int64     `json:"bytes"`       // copiado
	TotalBytes int64     `json:"total_bytes"` // total conocido hasta ahora
	Speed      float64   `json:"speed"`       // bytes/s
	ETA        *int64    `json:"eta"`         // segundos, si rclone lo sabe
	Errors     int       `json:"errors"`
	Done       bool      `json:"done"`
	Updated    time.Time `json:"-"`
}

// clientCopyStaleAfter: una copia que deja de informar este tiempo se da por cortada.
const clientCopyStaleAfter = 10 * time.Minute

// copyRun acumula una copia entera: un envío puede tener varias carpetas y rclone
// empieza sus cuentas de cero en cada una.
type copyRun struct {
	last    ClientCopy
	started time.Time
	dests   []string
	perDest map[string]destTotals
}

type destTotals struct {
	bytes  int64
	errors int
}

var clientCopies struct {
	sync.Mutex
	m map[string]*copyRun
}

// ReportClientCopy guarda el último estado de una copia. Con Done la da por terminada
// y avisa por Telegram con el resumen.
func ReportClientCopy(c ClientCopy) {
	clientCopies.Lock()
	if clientCopies.m == nil {
		clientCopies.m = map[string]*copyRun{}
	}
	run := clientCopies.m[c.ID]
	if c.Done {
		delete(clientCopies.m, c.ID)
		clientCopies.Unlock()
		if run != nil {
			go notifyCopy(run, true, time.Now())
		}
		return
	}
	now := time.Now()
	if run == nil {
		run = &copyRun{started: now, perDest: map[string]destTotals{}}
		clientCopies.m[c.ID] = run
	}
	c.Updated = now
	run.last = c
	if _, seen := run.perDest[c.Dest]; !seen {
		run.dests = append(run.dests, c.Dest)
	}
	run.perDest[c.Dest] = destTotals{bytes: c.Bytes, errors: c.Errors}
	clientCopies.Unlock()
}

// SweepClientCopies da por cortadas las copias que llevan clientCopyStaleAfter sin
// informar (ventana cerrada, PC apagado...) y avisa. Se llama periódicamente.
func SweepClientCopies(now time.Time) {
	clientCopies.Lock()
	var stale []*copyRun
	for id, run := range clientCopies.m {
		if now.Sub(run.last.Updated) > clientCopyStaleAfter {
			stale = append(stale, run)
			delete(clientCopies.m, id)
		}
	}
	clientCopies.Unlock()
	for _, run := range stale {
		notifyCopy(run, false, now)
	}
}

// ClientCopies devuelve las copias con estado reciente (de hace menos de maxAge).
func ClientCopies(maxAge time.Duration) []ClientCopy {
	clientCopies.Lock()
	defer clientCopies.Unlock()
	var out []ClientCopy
	for _, run := range clientCopies.m {
		if time.Since(run.last.Updated) <= maxAge {
			out = append(out, run.last)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// copySummary es el texto del aviso de fin (o de corte) de una copia.
func copySummary(run *copyRun, finished bool, now time.Time) (title, desc string) {
	var bytes int64
	var errs int
	for _, t := range run.perDest {
		bytes += t.bytes
		errs += t.errors
	}
	where := "(sin nombre)"
	if len(run.dests) > 0 && run.dests[0] != "" {
		where = run.dests[0]
		if len(run.dests) > 1 {
			where += fmt.Sprintf(" y %d carpeta(s) más", len(run.dests)-1)
		}
	}
	dur := now.Sub(run.started)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s en %s", where, copySize(float64(bytes)), copyDuration(dur))
	if secs := dur.Seconds(); secs >= 1 && bytes > 0 {
		fmt.Fprintf(&b, " · media %s/s", copySize(float64(bytes)/secs))
	}
	if finished {
		fmt.Fprintf(&b, "\nErrores: %d", errs)
		if errs > 0 {
			b.WriteString(" (vuelve a enviar lo mismo: se salta lo ya subido)")
		}
		return "Copia terminada", b.String()
	}
	last := run.last
	if last.TotalBytes > 0 {
		fmt.Fprintf(&b, "\nSe quedó en %d%% de la carpeta en curso", int(last.Bytes*100/last.TotalBytes))
	}
	b.WriteString("\nLa copia dejó de informar hace más de 10 minutos (¿ventana cerrada o PC apagado?). Al volver a enviarla se salta lo ya subido.")
	return "Copia interrumpida", b.String()
}

func notifyCopy(run *copyRun, finished bool, now time.Time) {
	title, desc := copySummary(run, finished, now)
	icon := "✅"
	if !finished {
		icon = "⚠️"
	}
	_ = notifier.Alert("", icon, title, desc, "")
}

func copySize(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func copyDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h >= 24:
		return fmt.Sprintf("%d d %d h", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case m > 0:
		return fmt.Sprintf("%d min", m)
	}
	return "menos de un minuto"
}
