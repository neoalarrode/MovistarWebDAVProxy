package notifier

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// transport es el de todas las peticiones a Telegram: propio, para poder cerrar sus
// conexiones cuando la Pi cambia de red.
var transport = http.DefaultTransport.(*http.Transport).Clone()

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
