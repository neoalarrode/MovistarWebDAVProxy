package auth

import (
	"context"
	"os"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// ChaCha20 va delante, tras el GREASE; sin los AES de TLS 1.3; el resto en su orden.
func TestChachaFirst(t *testing.T) {
	in := []uint16{0x1a1a, utls.TLS_AES_128_GCM_SHA256, utls.TLS_AES_256_GCM_SHA384, utls.TLS_CHACHA20_POLY1305_SHA256,
		utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305}
	want := []uint16{0x1a1a, utls.TLS_CHACHA20_POLY1305_SHA256, utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
	got := chachaFirst(in)
	if len(got) != len(want) {
		t.Fatalf("%x", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orden %x, esperado %x", got, want)
		}
	}
}

// Prueba real contra Movistar (MOVISTAR_TLS_LIVE=1): con ChaCha delante se negocia
// ChaCha20 con la huella del modo web.
func TestChachaLiveMovistar(t *testing.T) {
	if os.Getenv("MOVISTAR_TLS_LIVE") == "" {
		t.Skip("MOVISTAR_TLS_LIVE no definido")
	}
	t.Setenv("TLS_PREFER_CHACHA", "true")
	t.Setenv("UTLS_CLIENT_HELLO", "chrome") // la huella del modo web (en la Pi: «Chrome-133»)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := dialTLSContextWithUTLSHTTP1(ctx, "tcp", "micloud.movistar.es:443")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st := c.(*utls.UConn).ConnectionState()
	t.Logf("cifrado negociado: %s", utls.CipherSuiteName(st.CipherSuite))
	if st.CipherSuite != utls.TLS_CHACHA20_POLY1305_SHA256 {
		t.Fatalf("esperado ChaCha20, negociado %s", utls.CipherSuiteName(st.CipherSuite))
	}
}
