package webdav

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// uploadGate reparte los huecos de subida a la nube (WEBDAV_MAX_UPLOADS) entre todas
// las subidas, entren por donde entren (montaje, SMB sobre el montaje, WebDAV
// directo, copia masiva). Las subidas marcadas como masivas (cabecera
// X-Upload-Priority: bulk, p. ej. la de un script de migración) solo usan los huecos
// que no esperan las normales y nunca ocupan el último: así lo que sube una persona
// empieza enseguida aunque la copia masiva esté llenando la cola.
type uploadGate struct {
	mu        sync.Mutex
	capacity  int
	used      int
	waiting   []chan struct{} // subidas normales en espera (primero en llegar, primero en salir)
	waitingLo []chan struct{} // subidas masivas en espera
}

func newUploadGate(capacity int) *uploadGate {
	if capacity < 1 {
		capacity = 1
	}
	return &uploadGate{capacity: capacity}
}

// bulkLimit es cuántos huecos pueden ocupar a la vez las subidas masivas.
func (g *uploadGate) bulkLimit() int {
	if g.capacity > 1 {
		return g.capacity - 1
	}
	return 1
}

func (g *uploadGate) canTake(bulk bool) bool {
	if g.used >= g.capacity {
		return false
	}
	if bulk {
		return len(g.waiting) == 0 && g.used < g.bulkLimit()
	}
	return true
}

// tryAcquire toma un hueco si lo hay ahora mismo.
func (g *uploadGate) tryAcquire(bulk bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.canTake(bulk) && (bulk || len(g.waiting) == 0) {
		g.used++
		return true
	}
	return false
}

// acquire espera un hueco respetando la prioridad.
func (g *uploadGate) acquire(bulk bool) {
	g.mu.Lock()
	if g.canTake(bulk) && (bulk || len(g.waiting) == 0) {
		g.used++
		g.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	if bulk {
		g.waitingLo = append(g.waitingLo, ch)
	} else {
		g.waiting = append(g.waiting, ch)
	}
	g.mu.Unlock()
	<-ch
}

// release libera un hueco y se lo da primero a las subidas normales en espera.
func (g *uploadGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.used--
	for g.used < g.capacity {
		switch {
		case len(g.waiting) > 0:
			ch := g.waiting[0]
			g.waiting = g.waiting[1:]
			g.used++
			close(ch)
		case len(g.waitingLo) > 0 && g.used < g.bulkLimit():
			ch := g.waitingLo[0]
			g.waitingLo = g.waitingLo[1:]
			g.used++
			close(ch)
		default:
			return
		}
	}
}

// queueRAMBudget es la RAM total que pueden ocupar los cuerpos de subida en memoria
// (búfer inicial, archivos pequeños y cola). Es dinámico: la mitad de la memoria
// disponible de la máquina (MemAvailable) por encima de una reserva de 1 GiB, más
// lo que ya ocupan; se recalcula como mucho cada 2 s. WEBDAV_QUEUE_RAM_MB, si se
// define, es el máximo. Lo que no cabe va a disco y, sin sitio, el cliente espera.
func queueRAMBudget() int64 {
	limit := int64(-1)
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("WEBDAV_QUEUE_RAM_MB"))); err == nil && v >= 0 {
		limit = int64(v) << 20
	}
	budget := dynamicRAMBudget()
	if limit >= 0 && (budget < 0 || budget > limit) {
		budget = limit
	}
	if budget < 0 {
		budget = 512 << 20 // sin /proc/meminfo (p. ej. macOS): valor fijo
	}
	return budget
}

var ramProbe struct {
	sync.Mutex
	at        time.Time
	available int64 // bytes; -1 si no se puede leer
}

// dynamicRAMBudget devuelve el presupuesto según la memoria libre, o -1 si no se sabe.
func dynamicRAMBudget() int64 {
	ramProbe.Lock()
	if time.Since(ramProbe.at) > 2*time.Second {
		ramProbe.available = memAvailable()
		ramProbe.at = time.Now()
	}
	avail := ramProbe.available
	ramProbe.Unlock()
	if avail < 0 {
		return -1
	}
	const reserve = 1 << 30
	free := (avail - reserve) / 2
	if free < 0 {
		free = 0
	}
	return queuedRAM.Load() + free
}

// memAvailable lee MemAvailable de /proc/meminfo (bytes), o -1.
func memAvailable() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return kb << 10
				}
			}
		}
	}
	return -1
}

var queuedRAM atomic.Int64

// reserveQueueRAM reserva n bytes del presupuesto de RAM de la cola.
func reserveQueueRAM(n int64) bool {
	for {
		cur := queuedRAM.Load()
		if cur+n > queueRAMBudget() {
			return false
		}
		if queuedRAM.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func releaseQueueRAM(n int64) {
	if n > 0 {
		queuedRAM.Add(-n)
	}
}

// uploadGateSet cambia el número de huecos (al arrancar, con WEBDAV_MAX_UPLOADS).
func uploadGateSet(capacity int) {
	if capacity < 1 {
		capacity = 1
	}
	uploads.mu.Lock()
	uploads.capacity = capacity
	uploads.mu.Unlock()
}

func (g *uploadGate) capacityNow() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capacity
}

// queueDiskBudget es el disco total que pueden ocupar las subidas en cola
// (WEBDAV_QUEUE_DISK_MB, 8192 MB por defecto). Con la RAM y el disco de la cola
// llenos, el proxy deja de leer del cliente (le llega velocidad 0) hasta que haya
// sitio o hueco de subida.
func queueDiskBudget() int64 {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("WEBDAV_QUEUE_DISK_MB"))); err == nil && v >= 0 {
		return int64(v) << 20
	}
	return 8192 << 20
}

var queuedDisk atomic.Int64

func reserveQueueDisk(n int64) bool {
	for {
		cur := queuedDisk.Load()
		if cur+n > queueDiskBudget() {
			return false
		}
		if queuedDisk.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func releaseQueueDisk(n int64) {
	if n > 0 {
		queuedDisk.Add(-n)
	}
}
