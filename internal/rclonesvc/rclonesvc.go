// Package rclonesvc arranca y vigila un rclone interno que sirve por WebDAV los
// archivos YA DESCIFRADOS: rclone crypt sobre rclone chunker sobre el WebDAV del
// propio proxy, con la clave guardada en el proxy (internal/vault). Lo que suba un
// cliente a este WebDAV se cifra, se trocea y pasa por las colas y límites del proxy.
package rclonesvc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"movistar-webdav-proxy/internal/cryptname"
	"movistar-webdav-proxy/internal/logger"
	"movistar-webdav-proxy/internal/vault"
)

// Options configura el servicio.
type Options struct {
	Binary      string // ejecutable de rclone (RCLONE_BINARY, por defecto «rclone»)
	UpstreamURL string // WebDAV del propio proxy, p. ej. http://127.0.0.1:8080
	User, Pass  string // credenciales del WebDAV del proxy (y del descifrado)
	Addr        string // dirección del WebDAV descifrado (WEBDAV_DECRYPTED_ADDR, por defecto :8081)
	CacheMode   string // --vfs-cache-mode (WEBDAV_DECRYPTED_CACHE_MODE, por defecto off: subida directa)
	ConfigDir   string // dónde escribir rclone.conf (tmpfs; por defecto /tmp/rclone-proxy)

	// Montaje FUSE de los archivos descifrados (opcional), para que el sistema los
	// vea como una carpeta normal (Emby, Samba...). Requiere /dev/fuse y SYS_ADMIN.
	MountPath  string   // WEBDAV_MOUNT_PATH, p. ej. /mnt/emby_cloud
	MountArgs  []string // WEBDAV_MOUNT_ARGS (sustituye a las opciones por defecto)
	MountCache string   // WEBDAV_MOUNT_CACHE_DIR (por defecto /app/cache/rclone)
}

// DefaultMountArgs son las opciones del montaje probado en producción.
func DefaultMountArgs(cacheDir string) []string {
	return []string{"--allow-other", "--vfs-cache-mode", "writes", "--vfs-cache-max-size", "30G", "--vfs-cache-max-age", "1h",
		"--cache-dir", cacheDir, "--vfs-read-chunk-size", "32M", "--vfs-read-chunk-size-limit", "256M",
		"--dir-cache-time", "1000h", "--attr-timeout", "1h", "--umask", "022",
		"--rc", "--rc-addr", "127.0.0.1:5573", "--rc-no-auth"}
}

// Service vigila el proceso de rclone.
type Service struct {
	opts    Options
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	lastErr string

	mountRunning bool
	mountErr     string
}

// New crea el servicio con valores por defecto.
func New(o Options) *Service {
	if o.Binary == "" {
		o.Binary = "rclone"
	}
	if o.Addr == "" {
		o.Addr = ":8081"
	}
	if o.CacheMode == "" {
		o.CacheMode = "off"
	}
	if o.ConfigDir == "" {
		o.ConfigDir = filepath.Join(os.TempDir(), "rclone-proxy")
	}
	if o.MountCache == "" {
		o.MountCache = "/app/cache/rclone"
	}
	return &Service{opts: o}
}

// Status resume el estado para el panel.
func (s *Service) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{"running": s.running, "addr": s.opts.Addr, "cache_mode": s.opts.CacheMode, "error": s.lastErr,
		"mount_path": s.opts.MountPath, "mount_running": s.mountRunning, "mount_error": s.mountErr}
}

// Restart (re)arranca rclone con la clave actual; sin clave, lo detiene.
func (s *Service) Restart() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	c := vault.Current()
	if c == nil {
		s.running, s.lastErr = false, "sin clave de cifrado configurada"
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	go s.supervise(ctx, *c)
	if s.opts.MountPath != "" {
		go s.superviseMount(ctx, *c)
	}
}

// Stop detiene rclone.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// writeConfig escribe un rclone.conf (0600). «nube» es el WebDAV en bruto del
// proxy; cada carpeta cifrada de la raíz tiene su chunker + crypt; «raiz» las junta
// (combine) con el resto de carpetas de la raíz tal cual, para servir la nube entera
// desde la raíz con lo cifrado ya descifrado.
func (s *Service) writeConfig(c vault.Crypt, topDirs []string) (string, error) {
	if err := os.MkdirAll(s.opts.ConfigDir, 0o700); err != nil {
		return "", err
	}
	ob := func(v string) string {
		if v == "" {
			return ""
		}
		o, _ := cryptname.Obscure(v)
		return o
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[nube]\ntype = webdav\nurl = %s\nvendor = owncloud\nuser = %s\npass = %s\n\n", s.opts.UpstreamURL, s.opts.User, ob(s.opts.Pass))
	// Conviven carpetas cifradas y en claro: las de c.Projects() (las marcadas como
	// cifradas) se ven descifradas con la clave; el resto de la raíz, tal cual.
	encrypted := map[string]bool{}
	var upstreams []string
	for i, p := range c.Projects() {
		encrypted[p] = true
		fmt.Fprintf(&b, "[trozos-%d]\ntype = chunker\nremote = nube:%s\nchunk_size = %s\nname_format = %s\ntransactions = %s\nhash_type = none\nmeta_format = simplejson\n\n",
			i, p, c.ChunkSize, c.NameFormat, c.Transaction)
		fmt.Fprintf(&b, "[cifrada-%d]\ntype = crypt\nremote = trozos-%d:\nfilename_encryption = standard\ndirectory_name_encryption = true\npassword = %s\n", i, i, ob(c.Password))
		if c.Password2 != "" {
			fmt.Fprintf(&b, "password2 = %s\n", ob(c.Password2))
		}
		b.WriteString("\n")
		upstreams = append(upstreams, fmt.Sprintf("%q", fmt.Sprintf("%s=cifrada-%d:", p, i)))
	}
	for _, d := range topDirs {
		if d = strings.Trim(d, "/"); d != "" && !encrypted[d] {
			upstreams = append(upstreams, fmt.Sprintf("%q", fmt.Sprintf("%s=nube:%s", d, d)))
		}
	}
	fmt.Fprintf(&b, "[raiz]\ntype = combine\nupstreams = %s\n", strings.Join(upstreams, " "))
	path := filepath.Join(s.opts.ConfigDir, "rclone.conf")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// topDirs lista las carpetas de la raíz de la nube (WebDAV en bruto del proxy).
func (s *Service) topDirs(ctx context.Context, c vault.Crypt) []string {
	conf, err := s.writeConfig(c, nil)
	if err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(cctx, s.opts.Binary, "lsf", "--dirs-only", "--config", conf, "nube:").Output()
	if err != nil {
		logger.Log("[Descifrado] ⚠️ No se pudieron listar las carpetas de la raíz: %v", err)
		return nil
	}
	var dirs []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.Trim(strings.TrimSpace(l), "/"); l != "" {
			dirs = append(dirs, l)
		}
	}
	return dirs
}

// args son los argumentos de «rclone serve webdav».
func (s *Service) args(conf string) []string {
	a := []string{"serve", "webdav", "raiz:", "--config", conf,
		"--addr", s.opts.Addr, "--vfs-cache-mode", s.opts.CacheMode,
		"--dir-cache-time", "1m", "--log-level", "NOTICE"}
	if s.opts.User != "" {
		a = append(a, "--user", s.opts.User, "--pass", s.opts.Pass)
	}
	return a
}

func (s *Service) supervise(ctx context.Context, c vault.Crypt) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		conf, err := s.writeConfig(c, s.topDirs(ctx, c))
		if err != nil {
			s.setState(false, err.Error())
			return
		}
		cmd := exec.CommandContext(ctx, s.opts.Binary, s.args(conf)...)
		cmd.Env = append(os.Environ(), "RCLONE_CONFIG="+conf)
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			s.setState(false, "no se pudo arrancar rclone: "+err.Error())
			logger.Log("[Descifrado] ❌ No se pudo arrancar rclone (%s): %v", s.opts.Binary, err)
			return
		}
		s.setState(true, "")
		logger.Log("[Descifrado] 🔓 WebDAV en %s: la nube desde la raíz; cifradas %v, el resto en claro (caché %s)", s.opts.Addr, c.Projects(), s.opts.CacheMode)
		go pipeLog(out)
		start := time.Now()
		err = cmd.Wait()
		if ctx.Err() != nil {
			s.setState(false, "")
			return
		}
		s.setState(false, fmt.Sprintf("rclone terminó: %v", err))
		logger.Log("[Descifrado] ⚠️ rclone terminó (%v); se reinicia en %s", err, backoff)
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (s *Service) setState(running bool, errMsg string) {
	s.mu.Lock()
	s.running, s.lastErr = running, errMsg
	s.mu.Unlock()
}

func pipeLog(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			logger.Log("[Descifrado] %s", line)
		}
	}
}

// mountArgs son los argumentos de «rclone mount».
func (s *Service) mountArgs(conf string) []string {
	a := []string{"mount", "raiz:", s.opts.MountPath, "--config", conf}
	if len(s.opts.MountArgs) > 0 {
		return append(a, s.opts.MountArgs...)
	}
	return append(a, DefaultMountArgs(s.opts.MountCache)...)
}

// superviseMount mantiene montado WEBDAV_MOUNT_PATH con el rclone interno.
func (s *Service) superviseMount(ctx context.Context, c vault.Crypt) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		conf, err := s.writeConfig(c, s.topDirs(ctx, c))
		if err != nil {
			s.setMount(false, err.Error())
			return
		}
		_ = os.MkdirAll(s.opts.MountPath, 0o755)
		_ = os.MkdirAll(s.opts.MountCache, 0o700)
		// Un montaje anterior colgado (p. ej. tras un reinicio brusco) impide montar.
		_ = exec.Command("fusermount3", "-uz", s.opts.MountPath).Run()
		cmd := exec.CommandContext(ctx, s.opts.Binary, s.mountArgs(conf)...)
		cmd.Env = append(os.Environ(), "RCLONE_CONFIG="+conf)
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			s.setMount(false, "no se pudo arrancar rclone mount: "+err.Error())
			return
		}
		s.setMount(true, "")
		logger.Log("[Descifrado] 📂 Archivos descifrados montados en %s", s.opts.MountPath)
		go pipeLog(out)
		start := time.Now()
		err = cmd.Wait()
		_ = exec.Command("fusermount3", "-uz", s.opts.MountPath).Run()
		if ctx.Err() != nil {
			s.setMount(false, "")
			return
		}
		s.setMount(false, fmt.Sprintf("rclone mount terminó: %v", err))
		logger.Log("[Descifrado] ⚠️ rclone mount terminó (%v); se vuelve a montar en %s", err, backoff)
		if time.Since(start) > time.Minute {
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (s *Service) setMount(running bool, errMsg string) {
	s.mu.Lock()
	s.mountRunning, s.mountErr = running, errMsg
	s.mu.Unlock()
}

// CheckAuth comprueba las credenciales del WebDAV (las mismas del proxy).
func (s *Service) CheckAuth(user, pass string) bool {
	if s.opts.User == "" {
		return true
	}
	return user == s.opts.User && pass == s.opts.Pass
}

// WaitReady espera (hasta 60 s) a que el WebDAV interno acepte conexiones.
func (s *Service) WaitReady(ctx context.Context) {
	addr := s.opts.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}
