// Package autochunk ajusta solo el tamaño de trozo de las carpetas troceadas cuando
// Movistar rechaza las subidas grandes. Movistar limita a veces el tamaño por
// subida de una cuenta tras mucho volumen (p. ej. 100 MiB desde el 18/11/2026) y lo
// indica con MED-1048 («Multipart upload request without valid binary»). Cada
// rechazo baja el tamaño de trozo a uno que se sepa que pasa o a la mitad del
// rechazado; el valor se guarda en TOKENS_DIR/autochunk.json.
package autochunk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"movistar-webdav-proxy/internal/config"
)

// MinChunk es el mínimo al que se baja (por debajo, demasiados objetos en la nube).
const MinChunk int64 = 8 << 20

// State es el estado guardado.
type State struct {
	Chunk      int64     `json:"chunk"`       // tamaño de trozo en vigor (0 = el de la configuración)
	Rejected   int64     `json:"rejected"`    // subida más pequeña rechazada
	MaxOK      int64     `json:"max_ok"`      // subida más grande aceptada desde el primer rechazo
	Since      time.Time `json:"since"`       // primer rechazo
	LastChange time.Time `json:"last_change"` // último ajuste
	Strikes    int       `json:"strikes"`     // rechazos desde el último ajuste
}

// StrikesToChange es cuántos rechazos hacen falta para bajar: MED-1048 también
// puede salir suelto, como error pasajero que se resuelve al reintentar.
const StrikesToChange = 2

var (
	mu    sync.Mutex
	st    State
	onChg func()
)

func file() string { return filepath.Join(config.TokensDir, "autochunk.json") }

// Load lee el estado guardado.
func Load() {
	mu.Lock()
	defer mu.Unlock()
	if b, err := os.ReadFile(file()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
}

func saveLocked() {
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(config.TokensDir, 0o700)
	if os.WriteFile(file()+".tmp", b, 0o600) == nil {
		_ = os.Rename(file()+".tmp", file())
	}
}

// OnChange fija qué hacer cuando cambia el tamaño en vigor (recargar el rclone interno).
func OnChange(f func()) {
	mu.Lock()
	onChg = f
	mu.Unlock()
}

// Chunk devuelve el tamaño de trozo en vigor, o 0 si no hay ajuste.
func Chunk() int64 {
	mu.Lock()
	defer mu.Unlock()
	return st.Chunk
}

// Current devuelve una copia del estado.
func Current() State {
	mu.Lock()
	defer mu.Unlock()
	return st
}

// Accepted anota una subida aceptada de size bytes.
func Accepted(size int64) {
	mu.Lock()
	defer mu.Unlock()
	if !st.Since.IsZero() && size > st.MaxOK && (st.Rejected == 0 || size < st.Rejected) {
		st.MaxOK = size
		saveLocked()
	}
}

// Rejected anota una subida de size bytes rechazada por tamaño y, si hace falta,
// baja el tamaño de trozo. current es el tamaño de trozo con el que se subía (0 si
// no se sabe). Devuelve el nuevo tamaño si ha cambiado.
func Rejected(size, current int64) (int64, bool) {
	mu.Lock()
	if st.Since.IsZero() {
		st.Since = time.Now()
	}
	if st.Rejected == 0 || size < st.Rejected {
		st.Rejected = size
	}
	if st.MaxOK >= st.Rejected {
		st.MaxOK = 0 // lo aceptado antes ya no vale como referencia
	}
	limit := st.Chunk
	if limit == 0 || (current > 0 && current < limit) {
		limit = current
	}
	if limit == 0 {
		limit = size
	}
	if size < limit {
		limit = size
	}
	// Lo más grande que se sabe que pasa o, si no, la mitad del rechazo.
	next := st.Rejected / 2
	if st.MaxOK > next && st.MaxOK < st.Rejected {
		next = st.MaxOK
	}
	next = next &^ (1<<20 - 1) // en MiB enteros
	if next < MinChunk {
		next = MinChunk
	}
	changed := next < limit && (st.Chunk == 0 || next < st.Chunk)
	if changed {
		st.Strikes++
		changed = st.Strikes >= StrikesToChange
	}
	if changed {
		st.Strikes = 0
		st.Chunk = next
		st.LastChange = time.Now()
	}
	saveLocked()
	f := onChg
	mu.Unlock()
	if changed && f != nil {
		f()
	}
	return next, changed
}

// Reset vuelve al tamaño de trozo de la configuración.
func Reset() {
	mu.Lock()
	st = State{}
	saveLocked()
	f := onChg
	mu.Unlock()
	if f != nil {
		f()
	}
}
