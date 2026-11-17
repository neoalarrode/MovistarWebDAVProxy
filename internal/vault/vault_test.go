package vault

import (
	"os"
	"testing"

	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/cryptname"
)

func TestSaveLoad_EncryptedAndObscuredInput(t *testing.T) {
	old := config.TokensDir
	config.TokensDir = t.TempDir()
	t.Cleanup(func() { config.TokensDir = old })
	t.Setenv("TOKENS_ENCRYPTION_KEY", "llave-de-prueba")

	obs, _ := cryptname.Obscure("mi clave")
	if err := Save(Crypt{Password: obs, Password2: "sal"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file())
	if string(b) == "" || containsAny(string(b), "mi clave", "sal\"") {
		t.Fatalf("las contraseñas no deben quedar en claro en disco: %s", b)
	}
	mu.Lock()
	current = nil
	mu.Unlock()
	c, err := Load()
	if err != nil || c == nil || c.Password != "mi clave" || c.Password2 != "sal" || c.Root != "emby" || c.NameFormat != "*-chunk###" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestGenerate(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if a.Password == "" || a.Password == b.Password || a.Password2 == b.Password2 {
		t.Fatal("las claves generadas deben ser distintas y no vacías")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}
