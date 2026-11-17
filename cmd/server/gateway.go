package main

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
	"movistar-webdav-proxy/internal/webdav"
)

// decryptedGateway es el WebDAV público. Sin carpetas configuradas lo atiende el
// WebDAV en bruto; con alguna, reenvía todo al rclone interno (cada carpeta según
// su configuración y el resto tal cual) salvo crear una carpeta en la raíz, que
// rclone no puede hacer con «combine»: esa la crea el proxy en la nube y recarga
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
	if len(vault.Folders()) == 0 {
		g.raw.ServeHTTP(w, r)
		return
	}
	if r.Method == "MKCOL" {
		clean := strings.Trim(path.Clean("/"+r.URL.Path), "/")
		if clean != "" && !strings.Contains(clean, "/") {
			g.mkdirRoot(w, r, clean)
			return
		}
	}
	if r.Method == http.MethodPut {
		if raw, ok := rawPathFor(r.URL.Path); ok {
			// rclone no reenvía la cabecera: se marca la ruta en bruto que va a escribir.
			if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Upload-Priority")), "bulk") {
				defer webdav.MarkBulkPath(raw)()
			}
			// rclone sube con la hora de subida: se conserva la fecha original del cliente.
			if mt := strings.TrimSpace(r.Header.Get("X-OC-Mtime")); mt != "" {
				defer webdav.ExpectUploadMtime(raw, mt)()
			}
			// rclone sube en flujo sin Content-Length: se le dice al WebDAV en bruto el
			// tamaño de cada objeto para que suba directo a la nube.
			if size := r.ContentLength; size >= 0 {
				if sz := sizerFor(r.URL.Path); sz != nil {
					dir, base := path.Split("/" + raw)
					defer webdav.ExpectUploadSize(raw, func(p string) int64 {
						pd, name := path.Split(p)
						if pd != dir {
							return -1
						}
						return sz.sizeOf(size, base, name)
					})()
				}
			}
		}
	}
	if r.Method == http.MethodPut {
		sw := &statusWriter{ResponseWriter: w}
		g.upstream.ServeHTTP(sw, r)
		if sw.status >= 200 && sw.status < 300 {
			// rclone guarda en caché la hora de subida; la nube ya tiene la fecha
			// original (X-OC-Mtime): que la vuelva a leer.
			if err := rclonesvc.Forget(path.Clean("/" + r.URL.Path)); err != nil {
				logger.Log("[Descifrado] ⚠️ No se pudo refrescar '%s' en el rclone interno: %v", r.URL.Path, err)
			}
		}
		return
	}
	g.upstream.ServeHTTP(w, r)
}

// statusWriter guarda el código de respuesta.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// rawPathFor traduce una ruta del WebDAV público a la ruta en bruto en la nube:
// igual en las carpetas sin configuración y con los nombres cifrados en las que
// cifran nombres con el formato estándar de rclone crypt.
func rawPathFor(p string) (string, bool) {
	clean := strings.Trim(path.Clean("/"+p), "/")
	folder, rest, _ := strings.Cut(clean, "/")
	configured := false
	for _, f := range vault.Folders() {
		if f.Name == folder {
			configured = true
			break
		}
	}
	if !configured {
		return clean, true
	}
	v, ok := folderNames.Load(folder)
	if !ok {
		return "", false
	}
	c := v.(*cryptname.Cipher)
	segs := []string{folder}
	for _, s := range strings.Split(rest, "/") {
		if s != "" {
			segs = append(segs, c.EncryptSegment(s))
		}
	}
	return strings.Join(segs, "/"), true
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

// sizerFor devuelve cómo calcular los tamaños en bruto en la carpeta de p (sin
// configuración: tal cual).
func sizerFor(p string) *uploadSizer {
	folder, _, _ := strings.Cut(strings.Trim(path.Clean("/"+p), "/"), "/")
	for _, f := range vault.Folders() {
		if f.Name == folder {
			if v, ok := folderSizers.Load(folder); ok {
				return v.(*uploadSizer)
			}
			return nil
		}
	}
	return &uploadSizer{}
}
