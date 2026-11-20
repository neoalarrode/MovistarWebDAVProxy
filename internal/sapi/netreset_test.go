package sapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Una petición colgada (red muerta) se corta al momento con ResetConnections.
func TestResetConnectionsCancelsInflight(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	client := &http.Client{Transport: newResettableTransport(http.DefaultTransport.(*http.Transport).Clone())}
	errc := make(chan error, 1)
	go func() {
		_, err := client.Get(srv.URL)
		errc <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if n := ResetConnections("prueba"); n != 1 {
		t.Fatalf("peticiones cortadas: %d", n)
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("la petición cortada debe dar error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("la petición no se cortó")
	}
	if !NetworkChangedWithin(time.Minute) {
		t.Fatal("debe quedar anotado el cambio de red")
	}
}

// Una subida parada sola cuenta como rechazo; varias a la vez o tras un cambio de red, no.
func TestUploadStallCountsOnlyWhenAlone(t *testing.T) {
	var rejected atomic.Int32
	oldRej, oldSleep := UploadRejected, stallSleep
	UploadRejected = func(int64) { rejected.Add(1) }
	stallSleep = func(time.Duration) {}
	t.Cleanup(func() { UploadRejected, stallSleep = oldRej, oldSleep })
	reset := func() {
		stallLog.Lock()
		stallLog.at = nil
		stallLog.Unlock()
		inflight.Lock()
		inflight.changedAt = time.Time{}
		inflight.Unlock()
		rejected.Store(0)
	}

	reset()
	noteUploadStall(200<<20, time.Now())
	time.Sleep(100 * time.Millisecond)
	if rejected.Load() != 1 {
		t.Fatalf("una parada sola debe contar: %d", rejected.Load())
	}

	reset()
	now := time.Now()
	noteUploadStall(200<<20, now)
	noteUploadStall(200<<20, now.Add(time.Second))
	time.Sleep(100 * time.Millisecond)
	if rejected.Load() != 0 {
		t.Fatalf("dos paradas a la vez son la red: %d", rejected.Load())
	}

	reset()
	inflight.Lock()
	inflight.changedAt = time.Now()
	inflight.Unlock()
	noteUploadStall(200<<20, time.Now())
	time.Sleep(100 * time.Millisecond)
	if rejected.Load() != 0 {
		t.Fatalf("tras un cambio de red no cuenta: %d", rejected.Load())
	}
}
