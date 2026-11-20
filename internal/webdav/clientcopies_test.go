package webdav

import (
	"strings"
	"testing"
	"time"
)

// El resumen suma las carpetas de un mismo envío (rclone empieza de cero en cada una).
func TestCopySummary(t *testing.T) {
	t0 := time.Now().Add(-2 * time.Hour)
	run := &copyRun{started: t0, perDest: map[string]destTotals{}}
	for _, c := range []ClientCopy{
		{Dest: "Disney+/Serie A", Bytes: 1 << 30, Errors: 0},
		{Dest: "Disney+/Serie B", Bytes: 2 << 30, Errors: 1},
	} {
		run.dests = append(run.dests, c.Dest)
		run.perDest[c.Dest] = destTotals{bytes: c.Bytes, errors: c.Errors}
		run.last = c
	}
	title, desc := copySummary(run, true, t0.Add(2*time.Hour))
	if title != "Copia terminada" || !strings.Contains(desc, "Disney+/Serie A y 1 carpeta(s) más") ||
		!strings.Contains(desc, "3.0 GB en 2 h 0 min") || !strings.Contains(desc, "Errores: 1") {
		t.Fatalf("%s | %s", title, desc)
	}
	run.last = ClientCopy{Dest: "Disney+/Serie B", Bytes: 50, TotalBytes: 200}
	title, desc = copySummary(run, false, t0.Add(2*time.Hour))
	if title != "Copia interrumpida" || !strings.Contains(desc, "25%") {
		t.Fatalf("%s | %s", title, desc)
	}
}

// Una copia que deja de informar se da por cortada y desaparece; la que termina, también.
func TestClientCopyLifecycle(t *testing.T) {
	ReportClientCopy(ClientCopy{ID: "ciclo-1", Dest: "X", Bytes: 10, TotalBytes: 100})
	if len(ClientCopies(time.Minute)) == 0 {
		t.Fatal("debería estar en curso")
	}
	SweepClientCopies(time.Now().Add(clientCopyStaleAfter + time.Minute))
	for _, c := range ClientCopies(time.Hour) {
		if c.ID == "ciclo-1" {
			t.Fatal("una copia sin informar debe darse por cortada")
		}
	}
	ReportClientCopy(ClientCopy{ID: "ciclo-2", Dest: "Y"})
	ReportClientCopy(ClientCopy{ID: "ciclo-2", Done: true})
	for _, c := range ClientCopies(time.Hour) {
		if c.ID == "ciclo-2" {
			t.Fatal("al terminar debe desaparecer")
		}
	}
}
