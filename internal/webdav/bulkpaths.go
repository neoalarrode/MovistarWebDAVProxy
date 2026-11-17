package webdav

import (
	"strings"
	"sync"
)

// Rutas en bruto de subidas masivas que llegan por el WebDAV público: el rclone
// interno no reenvía la cabecera X-Upload-Priority, así que la pasarela marca aquí
// la ruta ya cifrada (el archivo y sus trozos empiezan por ella) mientras dura.
var bulkPaths struct {
	sync.Mutex
	m map[string]int
}

// MarkBulkPath marca como masiva toda subida en bruto cuya ruta empiece por p,
// hasta llamar a la función devuelta.
func MarkBulkPath(p string) func() {
	p = "/" + strings.Trim(p, "/")
	bulkPaths.Lock()
	if bulkPaths.m == nil {
		bulkPaths.m = map[string]int{}
	}
	bulkPaths.m[p]++
	bulkPaths.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			bulkPaths.Lock()
			if bulkPaths.m[p]--; bulkPaths.m[p] <= 0 {
				delete(bulkPaths.m, p)
			}
			bulkPaths.Unlock()
		})
	}
}

func isBulkPath(p string) bool {
	p = "/" + strings.Trim(p, "/")
	bulkPaths.Lock()
	defer bulkPaths.Unlock()
	for prefix := range bulkPaths.m {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
