package main

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/webdav"
)

// decryptedGateway es el WebDAV público cuando hay clave: reenvía todo al rclone
// interno (carpetas cifradas ya descifradas y el resto tal cual) salvo crear una
// carpeta en la raíz, que rclone no puede hacer con «combine»: esa la crea el proxy
// en la nube (en claro, salvo que esté marcada como cifrada en el panel) y recarga
// rclone para que aparezca.
type decryptedGateway struct {
	upstream *httputil.ReverseProxy
	raw      *webdav.Server
	svc      *rclonesvc.Service
}

func newDecryptedGateway(rcloneAddr string, raw *webdav.Server, svc *rclonesvc.Service) *decryptedGateway {
	target, _ := url.Parse("http://" + rcloneAddr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // descargas y subidas en flujo, sin acumular
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Log("[Descifrado] ⚠️ %s %s: rclone interno no responde: %v", r.Method, r.URL.Path, err)
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Servicio descifrado no disponible, reintenta", http.StatusServiceUnavailable)
	}
	return &decryptedGateway{upstream: rp, raw: raw, svc: svc}
}

func (g *decryptedGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "MKCOL" {
		clean := strings.Trim(path.Clean("/"+r.URL.Path), "/")
		if clean != "" && !strings.Contains(clean, "/") {
			g.mkdirRoot(w, r, clean)
			return
		}
	}
	g.upstream.ServeHTTP(w, r)
}

func (g *decryptedGateway) mkdirRoot(w http.ResponseWriter, r *http.Request, name string) {
	// La autenticación la comprueba el WebDAV en bruto con las mismas credenciales.
	if user, pass, ok := r.BasicAuth(); !ok || !g.svc.CheckAuth(user, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	fs := g.raw.FS()
	if fs == nil {
		http.Error(w, "no disponible", http.StatusServiceUnavailable)
		return
	}
	if err := fs.Mkdir(context.Background(), "/"+name, 0o755); err != nil {
		logger.Log("[Descifrado] ❌ No se pudo crear la carpeta raíz '%s': %v", name, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	logger.Log("[Descifrado] 📁 Nueva carpeta en la raíz: '%s'", name)
	g.svc.Restart()
	g.svc.WaitReady(context.Background())
	w.WriteHeader(http.StatusCreated)
}
