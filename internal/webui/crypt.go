package webui

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"

	"movistar-webdav-proxy/internal/autochunk"
	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/rcloneconf"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
)

// SetCrypt conecta el panel con el rclone interno.
func (s *Server) SetCrypt(svc *rclonesvc.Service) { s.crypt = svc }

type cryptFolder struct {
	Name       string   `json:"name"`
	Configured bool     `json:"configured"`
	Encrypted  bool     `json:"encrypted"`
	Types      []string `json:"types,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// rootDirs lista las carpetas de la raíz de la nube.
func (s *Server) rootDirs() []string {
	if s.webdavServer == nil || s.webdavServer.FS() == nil {
		return nil
	}
	f, err := s.webdavServer.FS().OpenFile(context.Background(), "/", os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		return nil
	}
	var out []string
	for _, fi := range infos {
		if fi.IsDir() {
			out = append(out, fi.Name())
		}
	}
	return out
}

// handleCryptStatus: carpetas de la raíz y su configuración (sin contraseñas).
func (s *Server) handleCryptStatus(w http.ResponseWriter, r *http.Request) {
	byName := map[string]*cryptFolder{}
	for _, d := range s.rootDirs() {
		byName[d] = &cryptFolder{Name: d}
	}
	for _, f := range vault.Folders() {
		cf := &cryptFolder{Name: f.Name, Configured: true}
		if ch, err := rcloneconf.Remap(f.Config, f.Top, "nube:"+f.Name, ""); err != nil {
			cf.Error = err.Error()
		} else {
			cf.Types = ch.Types
			cf.Encrypted = ch.Crypt() != nil
		}
		byName[f.Name] = cf
	}
	var list []*cryptFolder
	for _, f := range byName {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := map[string]interface{}{"folders": list, "secrets_encrypted": config.SecretsEncrypted(), "autochunk": autochunk.Current()}
	if s.crypt != nil {
		out["service"] = s.crypt.Status()
	}
	jsonResponse(w, http.StatusOK, out)
}

func (s *Server) restartCrypt() {
	if s.crypt != nil {
		s.crypt.Restart()
	}
}

// handleCryptFolder guarda la configuración de rclone (texto de rclone.conf) de una
// carpeta de la raíz y recarga el rclone interno.
func (s *Server) handleCryptFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	var f vault.Folder
	if !decodeJSONBody(w, r, &f) {
		return
	}
	ch, err := vault.SaveFolder(f)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	s.restartCrypt()
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "types": ch.Types, "encrypted": ch.Crypt() != nil})
}

// handleCryptFolderDelete quita la configuración de una carpeta (se sirve tal cual).
func (s *Server) handleCryptFolderDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if err := vault.DeleteFolder(req.Name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	s.restartCrypt()
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

// handleCryptGenerate crea una clave nueva para una carpeta sin configurar (crypt
// sobre chunker, como la recomendada), la guarda y devuelve UNA vez su configuración
// para que el usuario la guarde: sin ella no se puede leer nada de lo subido.
func (s *Server) handleCryptGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	for _, f := range vault.Folders() {
		if f.Name == req.Name {
			jsonResponse(w, http.StatusConflict, map[string]interface{}{"status": "error", "message": "Esa carpeta ya tiene configuración: cambiar la clave dejaría ilegible lo ya subido."})
			return
		}
	}
	c, err := vault.Generate()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	text := vault.StandardConfig(c)
	if _, err := vault.SaveFolder(vault.Folder{Name: req.Name, Config: text}); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	s.restartCrypt()
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "config": text})
}

// handleMediaDiagnostics (solo lectura): lo que SAPI dice de uno o varios archivos
// (?id=1&id=2...), para diagnosticar archivos sin URL de descarga.
func (s *Server) handleMediaDiagnostics(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["id"]
	if len(ids) == 0 || len(ids) > 50 || s.client == nil {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": "indica de 1 a 50 ?id="})
		return
	}
	var out []interface{}
	for _, id := range ids {
		d, err := s.client.MediaDiagnostics(id)
		if err != nil {
			out = append(out, map[string]interface{}{"id": id, "error": err.Error()})
			continue
		}
		out = append(out, d)
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "media": out})
}

// handleCryptMkdir crea una carpeta en la raíz de la nube (en claro; luego se le
// puede pegar su configuración o cifrar con clave nueva) y recarga el rclone interno.
func (s *Server) handleCryptMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	name := strings.Trim(strings.TrimSpace(req.Name), "/")
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": "Indica un nombre de carpeta sin «/»."})
		return
	}
	if s.webdavServer == nil || s.webdavServer.FS() == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]interface{}{"status": "error", "message": "El WebDAV aún no está listo."})
		return
	}
	for _, d := range s.rootDirs() {
		if strings.EqualFold(d, name) {
			jsonResponse(w, http.StatusConflict, map[string]interface{}{"status": "error", "message": "Ya existe esa carpeta."})
			return
		}
	}
	if err := s.webdavServer.FS().Mkdir(context.Background(), "/"+name, 0o755); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	s.restartCrypt()
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

// handleAutoChunkReset vuelve al tamaño de trozo de la configuración (si Movistar ya
// no limita el tamaño de las subidas).
func (s *Server) handleAutoChunkReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	autochunk.Reset()
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}
