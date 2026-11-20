package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
)

// Sin credenciales la pasarela responde 401 sin llegar al rclone interno (y sin anotar
// tamaños ni fechas de la ruta); con ellas, pasa.
func TestGatewayRequiresAuthFirst(t *testing.T) {
	old := config.TokensDir
	config.TokensDir = t.TempDir()
	t.Cleanup(func() { config.TokensDir = old })
	if err := vault.LoadFolders(); err != nil {
		t.Fatal(err)
	}
	conf := "[x-trozos]\ntype = chunker\nremote = nube:Fotos\n\n[x-cifrada]\ntype = crypt\nremote = x-trozos:\npassword = abc\n"
	if _, err := vault.SaveFolder(vault.Folder{Name: "Fotos", Config: conf}); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer up.Close()
	g := newDecryptedGateway(strings.TrimPrefix(up.URL, "http://"), nil, rclonesvc.New(rclonesvc.Options{User: "u", Pass: "p"}))

	req := httptest.NewRequest(http.MethodPut, "/Fotos/a.jpg", strings.NewReader("x"))
	req.Header.Set("X-OC-Mtime", "1600000000")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || hits.Load() != 0 {
		t.Fatalf("sin credenciales: %d, llegó al interno %d veces", rec.Code, hits.Load())
	}
	req = httptest.NewRequest(http.MethodPut, "/Fotos/a.jpg", strings.NewReader("x"))
	req.SetBasicAuth("u", "mala")
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || hits.Load() != 0 {
		t.Fatalf("contraseña mala: %d", rec.Code)
	}
	req = httptest.NewRequest("PROPFIND", "/Fotos/", nil)
	req.SetBasicAuth("u", "p")
	rec = httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if hits.Load() != 1 {
		t.Fatalf("con credenciales no llegó al interno (código %d)", rec.Code)
	}
}
