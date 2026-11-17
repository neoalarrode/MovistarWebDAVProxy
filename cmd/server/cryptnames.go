package main

import (
	"os"
	"strings"

	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
	"movistar-webdav-proxy/internal/webdav"
)

// prepareVault carga la clave de cifrado guardada en el proxy (internal/vault) o
// la importa una vez de RCLONE_CRYPT_PASSWORD[2]/RCLONE_CRYPT_ROOT (como en
// rclone.conf, ofuscadas o en claro). Devuelve si hay clave.
func prepareVault() bool {
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
	return vault.Current() != nil
}

// setupCrypt arranca, si hay clave, el rclone interno que sirve en el puerto WebDAV
// público los archivos descifrados (sobre el WebDAV en bruto de rawPort) y el
// descifrado de nombres del panel.
// internalDecryptedAddr es donde escucha el rclone interno; delante va el WebDAV
// público del proxy (decryptedGateway).
const internalDecryptedAddr = "127.0.0.1:8091"

func setupCrypt(webdavPort, rawPort string) *rclonesvc.Service {
	webdav.SetDisplayName(func(p string) string {
		c := vault.Current()
		n := vault.Names()
		if c == nil || n == nil {
			return ""
		}
		// Solo se descifra lo que está dentro de una carpeta marcada como cifrada.
		for _, project := range c.Projects() {
			if d := n.DecryptPath(p, project); d != "" {
				return project + "/" + d
			}
		}
		return ""
	})

	pass := os.Getenv("WEBDAV_PASSWORD")
	if pass == "" && os.Getenv("WEBDAV_PASSWORD_HASH") != "" {
		logger.Log("[Descifrado] ⚠️ Con WEBDAV_PASSWORD_HASH el rclone interno no puede autenticarse contra el proxy: usa WEBDAV_PASSWORD.")
	}
	svc := rclonesvc.New(rclonesvc.Options{
		Binary:      os.Getenv("RCLONE_BINARY"),
		UpstreamURL: "http://127.0.0.1:" + rawPort,
		User:        os.Getenv("WEBDAV_USER"),
		Pass:        pass,
		Addr:        internalDecryptedAddr,
		CacheMode:   os.Getenv("WEBDAV_DECRYPTED_CACHE_MODE"),
		MountPath:   strings.TrimSpace(os.Getenv("WEBDAV_MOUNT_PATH")),
		MountArgs:   strings.Fields(os.Getenv("WEBDAV_MOUNT_ARGS")),
		MountCache:  strings.TrimSpace(os.Getenv("WEBDAV_MOUNT_CACHE_DIR")),
	})
	if vault.Current() != nil {
		svc.Restart()
	}
	return svc
}
