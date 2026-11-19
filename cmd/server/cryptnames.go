package main

import (
	"context"
	"os"
	"strings"
	"sync"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/rcloneconf"
	"movistar-webdav-proxy/internal/rclonesvc"
	"movistar-webdav-proxy/internal/vault"
	"movistar-webdav-proxy/internal/webdav"
)

// prepareVault carga las configuraciones de rclone por carpeta guardadas en el
// proxy. La primera vez importa la clave de RCLONE_CRYPT_PASSWORD[2]/RCLONE_CRYPT_ROOT
// (como en rclone.conf, ofuscadas o en claro) o la de crypt.json como configuración
// de esa carpeta.
func prepareVault() {
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
	if err := vault.LoadFolders(); err != nil {
		logger.Log("[Descifrado] ⚠️ %v", err)
	}
	for _, f := range vault.Folders() {
		logger.Log("[Descifrado] 📁 Carpeta «%s» con configuración propia de rclone", f.Name)
	}
}

// internalDecryptedAddr es donde escucha el rclone interno; delante va el WebDAV
// público del proxy (decryptedGateway).
const internalDecryptedAddr = "127.0.0.1:8091"

// folderNames guarda el descifrador de nombres de cada carpeta configurada.
var folderNames sync.Map // nombre de carpeta → *cryptname.Cipher (o nil)

// folderSizers guarda cómo calcular los tamaños en bruto de cada carpeta configurada.
var folderSizers sync.Map // nombre de carpeta → *uploadSizer

func refreshFolderNames() {
	folderNames.Range(func(k, _ interface{}) bool { folderNames.Delete(k); return true })
	folderSizers.Range(func(k, _ interface{}) bool { folderSizers.Delete(k); return true })
	for _, f := range vault.Folders() {
		if n := vault.NamesFor(f); n != nil {
			folderNames.Store(f.Name, n)
		}
		if ch, err := rcloneconf.Remap(f.Config, f.Top, "nube:"+f.Name, ""); err == nil {
			if sz := newUploadSizer(ch); sz != nil {
				folderSizers.Store(f.Name, sz)
			}
		}
	}
}

// setupCrypt arranca el rclone interno que sirve en el puerto WebDAV público la
// nube con cada carpeta según su configuración, y el descifrado de nombres del panel.
func setupCrypt(webdavPort, rawPort string) *rclonesvc.Service {
	refreshFolderNames()
	webdav.SetDisplayName(func(p string) string {
		var out string
		folderNames.Range(func(k, v interface{}) bool {
			if d := v.(*cryptname.Cipher).DecryptPath(p, k.(string)); d != "" {
				out = k.(string) + "/" + d
				return false
			}
			return true
		})
		return out
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
		OnRestart:   refreshFolderNames,
	})
	if len(vault.Folders()) > 0 {
		svc.Restart()
	}
	return svc
}

// cloudFolderLister lista, con sus nombres en claro, las carpetas que hay dentro de
// la carpeta de la copia en la nube (COPY_CLOUD_FOLDER, por defecto «emby»): las
// bibliotecas que muestra el panel. Sale del listado del proxy, sin pedir nada nuevo.
func cloudFolderLister(srv *webdav.Server) func() []string {
	return func() []string {
		folder := strings.Trim(strings.TrimSpace(os.Getenv("COPY_CLOUD_FOLDER")), "/")
		if folder == "" {
			folder = "emby"
		}
		fs := srv.FS()
		if fs == nil {
			return nil
		}
		f, err := fs.OpenFile(context.Background(), "/"+folder, os.O_RDONLY, 0)
		if err != nil {
			return nil
		}
		defer f.Close()
		infos, err := f.Readdir(-1)
		if err != nil {
			return nil
		}
		var c *cryptname.Cipher
		if v, ok := folderNames.Load(folder); ok {
			c = v.(*cryptname.Cipher)
		}
		names := []string{}
		for _, fi := range infos {
			if !fi.IsDir() {
				continue
			}
			name := fi.Name()
			if c != nil {
				d, err := c.DecryptSegment(name)
				if err != nil {
					continue // no es de esta clave
				}
				name = d
			}
			names = append(names, name)
		}
		return names
	}
}
