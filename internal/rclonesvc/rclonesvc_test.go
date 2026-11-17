package rclonesvc

import (
	"os"
	"strings"
	"testing"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/vault"
)

func TestWriteConfig_ThreeRemotesAndObscuredSecrets(t *testing.T) {
	s := New(Options{UpstreamURL: "http://127.0.0.1:8080", User: "u", Pass: "p", ConfigDir: t.TempDir()})
	path, err := s.writeConfig(vault.Crypt{Password: "clave", Password2: "sal", Root: "emby", ChunkSize: "1Gi", NameFormat: "*-chunk###", Transaction: "norename"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	conf := string(b)
	for _, want := range []string{"[nube]", "url = http://127.0.0.1:8080", "[nube-trozos]", "remote = nube:emby", "name_format = *-chunk###", "transactions = norename", "[nube-cifrada]", "remote = nube-trozos:", "directory_name_encryption = true"} {
		if !strings.Contains(conf, want) {
			t.Errorf("falta %q en:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "= clave") || strings.Contains(conf, "= sal") {
		t.Fatal("las contraseñas deben ir ofuscadas como en rclone.conf")
	}
	for _, line := range strings.Split(conf, "\n") {
		if strings.HasPrefix(line, "password = ") {
			if v, _ := cryptname.Reveal(strings.TrimPrefix(line, "password = ")); v != "clave" {
				t.Errorf("password ofuscada no válida: %q", v)
			}
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("permisos %v, esperados 0600", fi.Mode().Perm())
	}
}

// El montaje usa por defecto las mismas opciones que el de producción.
func TestMountArgs_DefaultsMatchProduction(t *testing.T) {
	s := New(Options{MountPath: "/mnt/emby_cloud"})
	got := strings.Join(s.mountArgs("/tmp/r.conf"), " ")
	want := "mount nube-cifrada: /mnt/emby_cloud --config /tmp/r.conf --allow-other --vfs-cache-mode writes --vfs-cache-max-size 30G --vfs-cache-max-age 1h --cache-dir /app/cache/rclone --vfs-read-chunk-size 32M --vfs-read-chunk-size-limit 256M --dir-cache-time 1000h --attr-timeout 1h --umask 022 --rc --rc-addr 127.0.0.1:5573 --rc-no-auth"
	if got != want {
		t.Errorf("args:\n%s\nesperado:\n%s", got, want)
	}
	s2 := New(Options{MountPath: "/x", MountArgs: []string{"--vfs-cache-mode", "off"}})
	if g := strings.Join(s2.mountArgs("c"), " "); g != "mount nube-cifrada: /x --config c --vfs-cache-mode off" {
		t.Errorf("WEBDAV_MOUNT_ARGS no sustituye: %s", g)
	}
}
