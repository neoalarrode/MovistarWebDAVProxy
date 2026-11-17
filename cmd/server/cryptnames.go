package main

import (
	"os"
	"strings"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/webdav"
)

// setupCryptNames activa, si se configura, el descifrado de nombres de rclone crypt
// para mostrarlos legibles en el panel:
//
//	RCLONE_CRYPT_PASSWORD / RCLONE_CRYPT_PASSWORD2  las de rclone.conf (ofuscadas)
//	RCLONE_CRYPT_OBSCURED=false                      si se dan en claro
//	RCLONE_CRYPT_ROOT                                carpeta WebDAV del remoto crypt (p. ej. /emby)
func setupCryptNames() {
	pass := strings.TrimSpace(os.Getenv("RCLONE_CRYPT_PASSWORD"))
	if pass == "" {
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
