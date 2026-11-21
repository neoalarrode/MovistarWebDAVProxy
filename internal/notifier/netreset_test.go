package notifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Un envío que falla por la red se reintenta una vez (la conexión vieja queda atrás).
func TestSendRetriesOnceAfterNetworkError(t *testing.T) {
	var n int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close() // como una conexión muerta: sin respuesta
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Start()
	defer srv.Close()
	oldBase, oldDelay := apiBase, sendRetryDelay
	apiBase, sendRetryDelay = srv.URL, time.Millisecond
	t.Cleanup(func() { apiBase, sendRetryDelay = oldBase, oldDelay })
	tn := &TelegramNotifier{botToken: "1:a", chatID: "42", client: newClient(5 * time.Second), lastAlerts: map[string]time.Time{}}
	if err := tn.SendAlert("", "🔌", "La Pi vuelve al cable", "x", ""); err != nil {
		t.Fatalf("debía enviarse al reintentar: %v", err)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("intentos = %d, quería 2", got)
	}
}

// ResetConnections corta la espera de getUpdates y el bot vuelve a escuchar al
// momento, sin esperar el plazo ni la pausa de error.
func TestResetConnectionsRestartsPoll(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			mu.Lock()
			polls++
			mu.Unlock()
			select { // espera larga, como el long polling real
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	old, oldDefault := apiBase, Default
	apiBase = srv.URL
	Default = &TelegramNotifier{client: srv.Client(), lastAlerts: map[string]time.Time{}}
	t.Cleanup(func() { apiBase, Default = old, oldDefault })
	Default.UpdateCredentials("123:abc", "42")
	done := make(chan struct{})
	go func() {
		PollCommands(ctx, nil, nil, func(string, string) string { return "" })
		close(done)
	}()
	count := func() int { mu.Lock(); defer mu.Unlock(); return polls }
	wait := func(want int) {
		deadline := time.Now().Add(3 * time.Second)
		for count() < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(1)
	ResetConnections()
	wait(2)
	if got := count(); got != 2 {
		t.Fatalf("getUpdates = %d tras el cambio de red, quería 2 (escucha nueva al momento)", got)
	}
	cancel()
	<-done
}

// Telegram va por HTTP/1.1: los avisos no comparten conexión con la espera de getUpdates.
func TestTransportIsHTTP1(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	tr := transport.Clone()
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 1 {
		t.Fatalf("protocolo = %s, quería HTTP/1.1", resp.Proto)
	}
}
