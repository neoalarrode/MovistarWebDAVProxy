package webui

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// El flujo envía el estado al conectar y otra vez en cuanto cambia un archivo.
func TestCopyEvents_PushesOnChange(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plan.txt", "A\nB\n")
	write("progress.log", "2026-11-14 10:00:00 INICIO A\n")
	s := &Server{copyDir: dir, reads: newReadTracker(time.Minute)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(s.handleCopyEvents))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %q", ct)
	}

	events := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				events <- line
			}
		}
	}()
	next := func() string {
		select {
		case e := <-events:
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("no llegó ningún evento")
			return ""
		}
	}
	if e := next(); !strings.Contains(e, `"current":"A"`) {
		t.Fatalf("primer evento: %s", e)
	}
	write("progress.log", "2026-11-14 10:00:00 INICIO A\n2026-11-14 10:05:00 FIN A rc=0\n2026-11-14 10:05:01 INICIO B\n")
	if e := next(); !strings.Contains(e, `"current":"B"`) {
		t.Fatalf("tras el cambio: %s", e)
	}
}
