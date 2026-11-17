package webdav

import (
	"strings"
	"sync"
)

// Tamaños esperados de subidas que llegan sin Content-Length (el rclone interno sube
// en flujo, en «chunked»). La pasarela conoce el tamaño real del archivo y registra
// aquí, para la ruta en bruto (cifrada) que se va a escribir, cómo calcular el de
// cada objeto (archivo o trozo). Con el tamaño, la subida va en flujo directo a la
// nube en vez de esperar a tenerla entera.
var expectSizes struct {
	sync.Mutex
	m map[*expectEntry]struct{}
}

type expectEntry struct {
	prefix string
	size   func(rawPath string) int64
}

// ExpectUploadSize registra size para las subidas en bruto cuya ruta empiece por
// prefix, hasta llamar a la función devuelta. size devuelve -1 si no lo sabe.
func ExpectUploadSize(prefix string, size func(rawPath string) int64) func() {
	e := &expectEntry{prefix: "/" + strings.Trim(prefix, "/"), size: size}
	expectSizes.Lock()
	if expectSizes.m == nil {
		expectSizes.m = map[*expectEntry]struct{}{}
	}
	expectSizes.m[e] = struct{}{}
	expectSizes.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			expectSizes.Lock()
			delete(expectSizes.m, e)
			expectSizes.Unlock()
		})
	}
}

// expectedUploadSize devuelve el tamaño registrado para una subida en bruto, o -1.
func expectedUploadSize(p string) int64 {
	p = "/" + strings.Trim(p, "/")
	expectSizes.Lock()
	defer expectSizes.Unlock()
	for e := range expectSizes.m {
		if strings.HasPrefix(p, e.prefix) {
			if n := e.size(p); n >= 0 {
				return n
			}
		}
	}
	return -1
}

// Fecha de modificación original de las subidas que llegan por el WebDAV público: el
// rclone interno sube cada objeto con la hora de subida (no conoce la del archivo),
// así que la pasarela registra aquí la X-OC-Mtime del cliente para la ruta en bruto.
var expectMtimes struct {
	sync.Mutex
	m map[*mtimeEntry]struct{}
}

type mtimeEntry struct{ prefix, mtime string }

// ExpectUploadMtime fija la X-OC-Mtime de las subidas en bruto cuya ruta empiece por
// prefix, hasta llamar a la función devuelta.
func ExpectUploadMtime(prefix, mtime string) func() {
	e := &mtimeEntry{prefix: "/" + strings.Trim(prefix, "/"), mtime: mtime}
	expectMtimes.Lock()
	if expectMtimes.m == nil {
		expectMtimes.m = map[*mtimeEntry]struct{}{}
	}
	expectMtimes.m[e] = struct{}{}
	expectMtimes.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			expectMtimes.Lock()
			delete(expectMtimes.m, e)
			expectMtimes.Unlock()
		})
	}
}

func expectedUploadMtime(p string) string {
	p = "/" + strings.Trim(p, "/")
	expectMtimes.Lock()
	defer expectMtimes.Unlock()
	for e := range expectMtimes.m {
		if strings.HasPrefix(p, e.prefix) {
			return e.mtime
		}
	}
	return ""
}
