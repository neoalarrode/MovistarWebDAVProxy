package notifier

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"
	"time"
)

// transport es el de todas las peticiones a Telegram: propio, para poder cerrar sus
// conexiones cuando la Pi cambia de red. Solo HTTP/1.1: con HTTP/2 todo va por una
// única conexión, la que tiene abierta la espera de getUpdates; al cambiar de red esa
// conexión no está «ociosa», no se cierra, y los avisos (y su reintento) se mandaban
// por ella, muerta, hasta agotar el plazo.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	// La copia hereda la configuración TLS que anuncia «h2»: sin quitarlo, el servidor
	// contesta en HTTP/2 y aquí se espera HTTP/1.1.
	if t.TLSClientConfig != nil {
		t.TLSClientConfig = t.TLSClientConfig.Clone()
	} else {
		t.TLSClientConfig = &tls.Config{}
	}
	t.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return t
}()

func newClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport}
}

// pollCancel corta la espera de getUpdates en curso.
var pollCancel struct {
	sync.Mutex
	f context.CancelFunc
}

// ResetConnections cierra las conexiones con Telegram y corta la espera de mensajes
// en curso: tras un cambio de red siguen abiertas por la red vieja y no responden
// (los avisos se quedaban sin enviar y el bot sordo hasta su plazo).
func ResetConnections() {
	transport.CloseIdleConnections()
	pollCancel.Lock()
	if pollCancel.f != nil {
		pollCancel.f()
	}
	pollCancel.Unlock()
}
