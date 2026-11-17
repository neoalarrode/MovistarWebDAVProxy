package main

import (
	"os"
	"strings"

	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
	"movistar-webdav-proxy/internal/webdav"
)

// setupCrypt carga la clave de cifrado guardada en el proxy (internal/vault) y, si
// hay, arranca el WebDAV descifrado interno y el descifrado de nombres del panel.
//
// Para pasar una clave existente sin usar el panel, al primer arranque se puede dar
// RCLONE_CRYPT_PASSWORD / RCLONE_CRYPT_PASSWORD2 (como en rclone.conf, ofuscadas o
// en claro) y RCLONE_CRYPT_ROOT: se guardan en el proxy y ya no hacen falta.
func setupCrypt(webdavPort string) *rclonesvc.Service {
	if _, err := vault.Load(); err != nil {
		logger.Log("[Descifrado] ⚠️ %v", err)
	}
	if vault.Current() == nil {
		if p := strings.TrimSpace(os.Getenv("RCLONE_CRYPT_PASSWORD")); p != "" {
			c := vault.Defaults()
			c.Password, c.Password2 = p, strings.TrimSpace(os.Getenv("RCLONE_CRYPT_PASSWORD2"))
			if r := strings.TrimSpace(os.Getenv("RCLONE_CRYPT_ROOT")); r != "" {
				c.Root = r
			}
			if err := vault.Save(c); err != nil {
				logger.Log("[Descifrado] ⚠️ No se pudo guardar la clave de RCLONE_CRYPT_PASSWORD: %v", err)
			} else {
				logger.Log("[Descifrado] 🔑 Clave de cifrado importada del entorno y guardada en el proxy.")
			}
		}
	}
	webdav.SetDisplayName(func(p string) string {
		c := vault.Current()
		n := vault.Names()
		if c == nil || n == nil {
			return ""
		}
		return n.DecryptPath(p, c.Root)
	})

	pass := os.Getenv("WEBDAV_PASSWORD")
	if pass == "" && os.Getenv("WEBDAV_PASSWORD_HASH") != "" {
		logger.Log("[Descifrado] ⚠️ Con WEBDAV_PASSWORD_HASH el rclone interno no puede autenticarse contra el proxy: usa WEBDAV_PASSWORD.")
	}
	svc := rclonesvc.New(rclonesvc.Options{
		Binary:      os.Getenv("RCLONE_BINARY"),
		UpstreamURL: "http://127.0.0.1:" + webdavPort,
		User:        os.Getenv("WEBDAV_USER"),
		Pass:        pass,
		Addr:        os.Getenv("WEBDAV_DECRYPTED_ADDR"),
		CacheMode:   os.Getenv("WEBDAV_DECRYPTED_CACHE_MODE"),
	})
	if vault.Current() != nil {
		svc.Restart()
	}
	return svc
}
