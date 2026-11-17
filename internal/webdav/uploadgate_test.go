package webdav

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/sapi"
)

// Las subidas masivas nunca ocupan el último hueco y ceden el turno a las normales.
func TestUploadGate_PriorityAndReserve(t *testing.T) {
	g := newUploadGate(3)
	if !g.tryAcquire(true) || !g.tryAcquire(true) {
		t.Fatal("las masivas deben poder usar 2 de 3 huecos")
	}
	if g.tryAcquire(true) {
		t.Fatal("una masiva no debe ocupar el último hueco")
	}
	if !g.tryAcquire(false) {
		t.Fatal("una subida normal debe tener el hueco reservado")
	}
	// Con todo ocupado esperan una normal y una masiva: al liberar, entra la normal.
	order := make(chan string, 2)
	go func() { g.acquire(true); order <- "masiva" }()
	time.Sleep(20 * time.Millisecond)
	go func() { g.acquire(false); order <- "normal" }()
	time.Sleep(20 * time.Millisecond)
	g.release()
	if got := <-order; got != "normal" {
		t.Fatalf("primero debía entrar la normal, entró %s", got)
	}
}

// Sin hueco libre la subida queda en cola (RAM) y, al liberarse un hueco, pasa a
// subirse en flujo con lo que ya tenía guardado.
func TestQueuedUpload_PromotesToStream(t *testing.T) {
	t.Setenv("WEBDAV_UPLOAD_SPOOL", "false")
	t.Setenv("WEBDAV_BUFFER_MB", "1")
	t.Setenv("WEBDAV_QUEUE_RAM_MB", "64")
	oldCap := uploads.capacityNow()
	uploadGateSet(1)
	t.Cleanup(func() { uploadGateSet(oldCap) })

	var mu sync.Mutex
	var got []byte
	var calls int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sapi/upload" {
			atomic.AddInt32(&calls, 1)
			_ = r.ParseMultipartForm(1 << 20)
			if f, _, err := r.FormFile("file"); err == nil {
				b, _ := io.ReadAll(f)
				mu.Lock()
				got = b
				mu.Unlock()
			}
			_, _ = io.WriteString(w, `{"data":{"id":"777","name":"t.bin"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"folders":[{"id":100,"name":"Root","parentid":0,"magic":true}]}}`)
	}))
	t.Cleanup(mock.Close)
	client, err := sapi.NewClient(config.AccountConfig{Provider: "movistar", Phone: "612345678", AccessToken: "a", RefreshToken: "r", ValidationKey: "vk"})
	if err != nil {
		t.Fatal(err)
	}
	client.SetBaseURLs(mock.URL+"/sapi", mock.URL)
	mfs := NewMovistarFS(client)

	if !uploads.tryAcquire(false) { // otro cliente ocupa el único hueco
		t.Fatal("no se pudo ocupar el hueco")
	}
	payload := bytes.Repeat([]byte{7}, 3<<20)
	w := NewWebDAVFileWriter(mfs, "/t.bin", "t.bin", 100, "")
	w.guard = &uploadGuard{expected: int64(len(payload))}
	if _, err := w.Write(payload[:2<<20]); err != nil {
		t.Fatal(err)
	}
	if !w.queued || w.streamOnly {
		t.Fatal("sin hueco libre la subida debe quedar en cola")
	}
	uploads.release() // queda libre el hueco
	if _, err := w.Write(payload[2<<20:]); err != nil {
		t.Fatal(err)
	}
	if !w.streamOnly {
		t.Fatal("con hueco libre debía pasar a subirse en flujo")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(got, payload) || calls != 1 {
		t.Fatalf("la nube recibió %d bytes en %d subidas, esperados %d en 1", len(got), calls, len(payload))
	}
	if queuedRAM.Load() != 0 {
		t.Errorf("la RAM de la cola debe quedar liberada, quedan %d", queuedRAM.Load())
	}
}

func TestBulkPaths(t *testing.T) {
	done := MarkBulkPath("emby/abc/def")
	if !isBulkPath("/emby/abc/def-chunk001") || !isBulkPath("/emby/abc/def") || isBulkPath("/emby/abc/otro") {
		t.Fatal("prefijo de subida masiva mal resuelto")
	}
	done2 := MarkBulkPath("/emby/abc/def/")
	done()
	done()
	if !isBulkPath("/emby/abc/def") {
		t.Fatal("la segunda marca debe seguir activa")
	}
	done2()
	if isBulkPath("/emby/abc/def") {
		t.Fatal("marca no liberada")
	}
}
