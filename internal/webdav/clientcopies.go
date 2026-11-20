package webdav

import (
	"sort"
	"sync"
	"time"
)

// ClientCopy es el estado que un cliente (p. ej. «Enviar a > Nube» en Windows) manda
// de su copia con rclone: el proxy solo ve lo que le llega, el cliente sabe el total.
type ClientCopy struct {
	ID         string    `json:"id"`
	Dest       string    `json:"dest"`        // carpeta de destino en claro, p. ej. «Netflix/Serie»
	Bytes      int64     `json:"bytes"`       // copiado
	TotalBytes int64     `json:"total_bytes"` // total conocido hasta ahora
	Speed      float64   `json:"speed"`       // bytes/s
	ETA        *int64    `json:"eta"`         // segundos, si rclone lo sabe
	Errors     int       `json:"errors"`
	Done       bool      `json:"done"`
	Updated    time.Time `json:"-"`
}

var clientCopies struct {
	sync.Mutex
	m map[string]ClientCopy
}

// ReportClientCopy guarda el último estado de una copia; con Done la quita.
func ReportClientCopy(c ClientCopy) {
	clientCopies.Lock()
	defer clientCopies.Unlock()
	if clientCopies.m == nil {
		clientCopies.m = map[string]ClientCopy{}
	}
	if c.Done {
		delete(clientCopies.m, c.ID)
		return
	}
	c.Updated = time.Now()
	clientCopies.m[c.ID] = c
	for id, old := range clientCopies.m {
		if time.Since(old.Updated) > 10*time.Minute {
			delete(clientCopies.m, id)
		}
	}
}

// ClientCopies devuelve las copias con estado reciente (de hace menos de maxAge).
func ClientCopies(maxAge time.Duration) []ClientCopy {
	clientCopies.Lock()
	defer clientCopies.Unlock()
	var out []ClientCopy
	for _, c := range clientCopies.m {
		if time.Since(c.Updated) <= maxAge {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
