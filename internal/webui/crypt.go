package webui

import (
	"net/http"

	"movistar-webdav-proxy/internal/config"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
)

// SetCrypt conecta el panel con el WebDAV descifrado interno.
func (s *Server) SetCrypt(svc *rclonesvc.Service) { s.crypt = svc }

// handleCryptStatus: estado de la clave y del WebDAV descifrado (sin contraseñas).
func (s *Server) handleCryptStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{"configured": false, "secrets_encrypted": config.SecretsEncrypted()}
	if c := vault.Current(); c != nil {
		out["configured"] = true
		out["root"] = c.Root
		out["chunk_size"] = c.ChunkSize
		out["name_format"] = c.NameFormat
		out["transactions"] = c.Transaction
	}
	if s.crypt != nil {
		out["service"] = s.crypt.Status()
	}
	jsonResponse(w, http.StatusOK, out)
}

// handleCryptConfig guarda una clave existente (la de un rclone.conf: ofuscada o
// en claro) y reinicia el WebDAV descifrado.
func (s *Server) handleCryptConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	var c vault.Crypt
	if !decodeJSONBody(w, r, &c) {
		return
	}
	if err := vault.Save(c); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	if s.crypt != nil {
		s.crypt.Restart()
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

// handleCryptGenerate crea una clave nueva, la guarda y la devuelve UNA vez para
// que el usuario la guarde: sin ella no se puede leer nada de lo subido.
func (s *Server) handleCryptGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]interface{}{"status": "error"})
		return
	}
	if vault.Current() != nil {
		jsonResponse(w, http.StatusConflict, map[string]interface{}{"status": "error", "message": "Ya hay una clave configurada: cambiarla dejaría ilegible lo ya subido. Introdúcela de nuevo si quieres sustituirla."})
		return
	}
	c, err := vault.Generate()
	if err == nil {
		err = vault.Save(c)
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}
	if s.crypt != nil {
		s.crypt.Restart()
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok", "password": c.Password, "password2": c.Password2, "root": c.Root})
}
