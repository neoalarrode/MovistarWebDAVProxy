package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/vault"
)

// La configuración guardada de una carpeta y las credenciales del WebDAV se pueden
// volver a ver desde el panel (solo por POST).
func TestCryptFolderConfigAndCredentials(t *testing.T) {
	old := config.TokensDir
	config.TokensDir = t.TempDir()
	t.Cleanup(func() { config.TokensDir = old })
	if err := vault.LoadFolders(); err != nil {
		t.Fatal(err)
	}
	conf := "[x-trozos]\ntype = chunker\nremote = nube:Fotos\nchunk_size = 96Mi\n\n[x-cifrada]\ntype = crypt\nremote = x-trozos:\npassword = abc\n"
	if _, err := vault.SaveFolder(vault.Folder{Name: "Fotos", Config: conf}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBDAV_USER", "usuario")
	t.Setenv("WEBDAV_PASSWORD", "secreta")
	s := &Server{}

	post := func(h http.HandlerFunc, body string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := post(s.handleCryptFolderConfig, `{"name":"Fotos"}`)
	if code != 200 || out["config"] != conf {
		t.Fatalf("config: %d %v", code, out)
	}
	if code, _ := post(s.handleCryptFolderConfig, `{"name":"Otra"}`); code != 404 {
		t.Fatalf("carpeta sin configuración: %d", code)
	}
	code, out = post(s.handleWebDAVCredentials, `{}`)
	if code != 200 || out["user"] != "usuario" || out["password"] != "secreta" {
		t.Fatalf("credenciales: %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	s.handleWebDAVCredentials(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code == 200 {
		t.Fatal("las credenciales no deben salir por GET")
	}
}
