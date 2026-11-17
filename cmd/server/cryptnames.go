package main

import (
	"os"
	"strings"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/vault"
	"movistar-webdav-proxy/internal/webdav"
)

// setupCryptNames activa, si se configura, el descifrado de nombres de rclone crypt
// para mostrarlos legibles en el panel:
//
//	RCLONE_CRYPT_PASSWORD / RCLONE_CRYPT_PASSWORD2  las de rclone.conf (ofuscadas)
//	RCLONE_CRYPT_OBSCURED=false                      si se dan en claro
//	RCLONE_CRYPT_ROOT                                carpeta WebDAV del remoto crypt (p. ej. /emby)
//
// Sin esas variables usa la clave guardada en el proxy (TOKENS_DIR/crypt.json).
func setupCryptNames() {
	pass := strings.TrimSpace(os.Getenv("RCLONE_CRYPT_PASSWORD"))
	if pass == "" {
		c, err := vault.Load()
		if err != nil {
			logger.Log("[WebDAV] ⚠️ No se pudo leer la clave de cifrado guardada: %v", err)
			return
		}
		if c == nil {
			return
		}
		names, err := cryptname.New(c.Password, c.Password2)
		if err != nil {
			logger.Log("[WebDAV] ⚠️ No se pudo preparar el descifrado de nombres: %v", err)
			return
		}
		webdav.SetDisplayName(func(p string) string { return names.DecryptPath(p, c.Root) })
		logger.Log("[WebDAV] 🔓 Nombres de rclone crypt descifrados en el panel con la clave guardada (raíz %q)", "/"+strings.Trim(c.Root, "/"))
		return
	}
	pass2 := strings.TrimSpace(os.Getenv("RCLONE_CRYPT_PASSWORD2"))
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("RCLONE_CRYPT_OBSCURED")), "false") {
		var err error
		if pass, err = cryptname.Reveal(pass); err != nil {
			logger.Log("[WebDAV] ⚠️ RCLONE_CRYPT_PASSWORD no es una contraseña ofuscada de rclone: %v", err)
			return
		}
		if pass2 != "" {
			if pass2, err = cryptname.Reveal(pass2); err != nil {
				logger.Log("[WebDAV] ⚠️ RCLONE_CRYPT_PASSWORD2 no es una contraseña ofuscada de rclone: %v", err)
				return
			}
		}
	}
	c, err := cryptname.New(pass, pass2)
	if err != nil {
		logger.Log("[WebDAV] ⚠️ No se pudo preparar el descifrado de nombres: %v", err)
		return
	}
	root := os.Getenv("RCLONE_CRYPT_ROOT")
	webdav.SetDisplayName(func(p string) string { return c.DecryptPath(p, root) })
	logger.Log("[WebDAV] 🔓 Nombres de rclone crypt descifrados en el panel (raíz %q)", "/"+strings.Trim(root, "/"))
}
