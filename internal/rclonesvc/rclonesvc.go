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
}

// Service vigila el proceso de rclone.
type Service struct {
	opts    Options
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	lastErr string
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
	return &Service{opts: o}
}

// Status resume el estado para el panel.
func (s *Service) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{"running": s.running, "addr": s.opts.Addr, "cache_mode": s.opts.CacheMode, "error": s.lastErr}
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

// writeConfig escribe un rclone.conf (0600) con los tres remotos.
func (s *Service) writeConfig(c vault.Crypt) (string, error) {
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
	fmt.Fprintf(&b, "[nube-trozos]\ntype = chunker\nremote = nube:%s\nchunk_size = %s\nname_format = %s\ntransactions = %s\nhash_type = none\nmeta_format = simplejson\n\n",
		c.Root, c.ChunkSize, c.NameFormat, c.Transaction)
	fmt.Fprintf(&b, "[nube-cifrada]\ntype = crypt\nremote = nube-trozos:\nfilename_encryption = standard\ndirectory_name_encryption = true\npassword = %s\n", ob(c.Password))
	if c.Password2 != "" {
		fmt.Fprintf(&b, "password2 = %s\n", ob(c.Password2))
	}
	path := filepath.Join(s.opts.ConfigDir, "rclone.conf")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// args son los argumentos de «rclone serve webdav».
func (s *Service) args(conf string) []string {
	a := []string{"serve", "webdav", "nube-cifrada:", "--config", conf,
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
		conf, err := s.writeConfig(c)
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
		logger.Log("[Descifrado] 🔓 WebDAV descifrado en %s (rclone crypt + chunker sobre el proxy, caché %s)", s.opts.Addr, s.opts.CacheMode)
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
