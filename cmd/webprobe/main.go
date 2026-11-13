// webprobe es un prototipo para validar el acceso a Movistar Cloud "como la web":
// un Chromium real con la sesión del usuario ejecuta las llamadas a SAPI con fetch()
// dentro de micloud.movistar.es, y Go descarga y sube directamente usando la URL
// firmada y las cookies de esa sesión.
//
// Uso:
//
//	webprobe -login          abre Chrome con ventana para iniciar sesión (una vez)
//	webprobe -remote-login :8099   sin ventana: sirve una página web para iniciar sesión a distancia
//	webprobe                 ejecuta las pruebas en modo headless con el perfil guardado
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"movistar-webdav-proxy/internal/webclient"
)

const portal = "https://micloud.movistar.es"

// sapiJS se inyecta en la página: replica cómo la web llama a SAPI
// (cookies de sesión + validationkey en la URL + X-deviceid "web-<huella>").
const sapiJS = `window.__sapi = async (method, path, body, deviceId) => {
  const m = document.cookie.match(/validationKey=([^;]+)/);
  const vk = m ? decodeURIComponent(m[1]) : '';
  const sep = path.includes('?') ? '&' : '?';
  const opts = {method, credentials: 'include', headers: {'Accept': 'application/json', 'X-deviceid': 'web-' + deviceId}};
  if (body !== null) { opts.headers['Content-Type'] = 'application/json;charset=UTF-8'; opts.body = JSON.stringify(body); }
  const r = await fetch(path + sep + 'validationkey=' + encodeURIComponent(vk), opts);
  const text = await r.text();
  return {status: r.status, body: text};
};`

type sapiResp struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type browser struct {
	ctx      context.Context
	deviceID string
}

func (b *browser) sapi(method, path string, body interface{}) (sapiResp, error) {
	bodyJSON := "null"
	if body != nil {
		bb, _ := json.Marshal(body)
		bodyJSON = string(bb)
	}
	expr := fmt.Sprintf("window.__sapi(%q, %q, %s, %q)", method, path, bodyJSON, b.deviceID)
	return chromedp.Run(b.ctx, chromedp.Evaluate[sapiResp](expr, chromedp.EvalAwaitPromise))
}

func loadDeviceID(dir string) string {
	p := filepath.Join(dir, "device-id")
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) == 32 {
		return string(bytes.TrimSpace(b))
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	_ = os.WriteFile(p, []byte(id), 0o600)
	return id
}

func main() {
	login := flag.Bool("login", false, "abrir Chrome con ventana para iniciar sesión")
	profile := flag.String("profile", ".webprofile", "directorio del perfil de Chrome")
	chunkName := flag.String("chunk", ".rclone_chunk.001_", "subcadena del archivo a usar en la prueba de descarga")
	remoteLogin := flag.String("remote-login", "", "dirección donde servir la página de inicio de sesión remoto (p. ej. :8099)")
	noSandbox := flag.Bool("no-sandbox", false, "lanzar Chromium sin sandbox (necesario como root en contenedores)")
	dropSession := flag.Bool("drop-session", false, "borrar JSESSIONID antes de empezar (simula la caducidad de las 24 h)")
	startPath := flag.String("path", "/", "ruta del portal con la que empezar (p. ej. /login)")
	chromePath := flag.String("chrome", "", "ruta del ejecutable de Chrome/Chromium (por defecto, autodetectar)")
	flag.Parse()

	if err := os.MkdirAll(*profile, 0o700); err != nil {
		log.Fatal(err)
	}
	absProfile, _ := filepath.Abs(*profile)

	// Chromium deja SingletonLock/Cookie/Socket atados al hostname; en Docker cada
	// contenedor tiene uno distinto y el perfil queda "en uso por otro ordenador".
	// Solo hay una instancia por perfil, así que se limpian al arrancar.
	stale, _ := filepath.Glob(filepath.Join(absProfile, "chrome", "Singleton*"))
	for _, f := range stale {
		_ = os.Remove(f)
	}

	opts := append([]chromedp.ExecAllocatorOption{},
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(filepath.Join(absProfile, "chrome")),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1280, 900),
	)
	if !*login {
		opts = append(opts, chromedp.Headless)
	}
	if goruntime.GOOS == "linux" {
		// Misma clave de cifrado de cookies en todos los arranques (sin llavero en el servidor).
		opts = append(opts, chromedp.Flag("password-store", "basic"))
	}
	if *noSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	if *chromePath != "" {
		opts = append(opts, chromedp.ExecPath(*chromePath))
	}
	actx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()

	b := &browser{ctx: ctx, deviceID: loadDeviceID(absProfile)}

	if *dropSession {
		if _, err := chromedp.Call(ctx, network.DeleteCookies, network.DeleteCookiesParams{Name: "JSESSIONID", Domain: ".micloud.movistar.es", Path: "/"}); err != nil {
			log.Fatalf("borrar JSESSIONID: %v", err)
		}
		log.Println("JSESSIONID borrada")
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(portal+*startPath)); err != nil {
		log.Fatalf("navegar: %v", err)
	}

	if *remoteLogin != "" {
		runRemoteLogin(ctx, b, *remoteLogin, absProfile)
		closeBrowser(ctx)
		return
	}

	if *login {
		log.Println("Inicia sesión en la ventana de Chrome. Espero hasta 10 minutos...")
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
			if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](sapiJS+";true")); err != nil {
				continue
			}
			r, err := b.sapi("GET", "/sapi/profile/role?action=get", nil)
			if err == nil && r.Status == 200 {
				log.Println("✔ Sesión iniciada. Perfil guardado en", absProfile)
				closeBrowser(ctx)
				return
			}
		}
		log.Fatal("no se detectó la sesión a tiempo")
	}

	time.Sleep(2 * time.Second)
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](sapiJS+";true")); err != nil {
		log.Fatalf("inyectar JS: %v", err)
	}
	ua, _ := chromedp.Run(ctx, chromedp.Evaluate[string]("navigator.userAgent"))
	log.Printf("UA headless: %s", ua)

	// 1. Sesión válida
	r, err := b.sapi("GET", "/sapi/profile/role?action=get", nil)
	must("profile/role", r, err)

	// 2. Raíz y carpetas
	r, err = b.sapi("GET", "/sapi/media/folder/root?action=get", nil)
	must("folder/root", r, err)
	var root struct {
		Data struct {
			Folders []struct {
				ID int64 `json:"id"`
			} `json:"folders"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(r.Body), &root)
	if len(root.Data.Folders) == 0 {
		log.Fatalf("raíz sin id: %s", trunc(r.Body))
	}
	rootID := root.Data.Folders[0].ID

	// 3. Recorrer hasta el chunk de prueba (primer archivo que contenga -chunk)
	t0 := time.Now()
	var item map[string]interface{}
	var calls int
	var walk func(folderID int64, depth int) bool
	walk = func(folderID int64, depth int) bool {
		if depth > 6 {
			return false
		}
		calls++
		r, err := b.sapi("POST", fmt.Sprintf("/sapi/media?action=get&folderid=%d&limit=200", folderID),
			map[string]interface{}{"data": map[string]interface{}{"fields": []string{"name", "size", "modificationdate"}}})
		must("media get folder", r, err)
		var media struct {
			Data struct {
				Media []map[string]interface{} `json:"media"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(r.Body), &media)
		for _, m := range media.Data.Media {
			if n, _ := m["name"].(string); strings.Contains(n, *chunkName) {
				item = m
				return true
			}
		}
		calls++
		r, err = b.sapi("GET", fmt.Sprintf("/sapi/media/folder?action=list&parentid=%d&limit=200", folderID), nil)
		must("folder list", r, err)
		var fl struct {
			Data struct {
				Folders []struct {
					ID int64 `json:"id"`
				} `json:"folders"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(r.Body), &fl)
		for _, f := range fl.Data.Folders {
			if walk(f.ID, depth+1) {
				return true
			}
		}
		return false
	}
	if !walk(rootID, 0) {
		log.Fatalf("no encontré ningún archivo con %q", *chunkName)
	}
	log.Printf("✔ Listado: %d llamadas en %s → %v (%v bytes)", calls, time.Since(t0).Round(time.Millisecond), item["name"], item["size"])

	// 4. URL de descarga firmada, pedida como la web
	id := fmt.Sprint(item["id"])
	r, err = b.sapi("POST", "/sapi/media?action=get&origin=omh,dropbox",
		map[string]interface{}{"data": map[string]interface{}{"fields": []string{"url"}, "ids": []string{id}}})
	must("media get url", r, err)
	var urlResp struct {
		Data struct {
			Media []struct {
				URL string `json:"url"`
			} `json:"media"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(r.Body), &urlResp)
	if len(urlResp.Data.Media) == 0 || urlResp.Data.Media[0].URL == "" {
		log.Fatalf("sin URL de descarga: %s", trunc(r.Body))
	}
	dl := urlResp.Data.Media[0].URL
	if strings.HasPrefix(dl, "/") {
		dl = portal + dl
	}
	log.Printf("✔ URL firmada obtenida (%s…)", dl[:strings.Index(dl, "k=")+2])

	// 5. Descarga directa desde Go, sin cookies, 64 MB a mitad de archivo
	const off, n = 512 << 20, 64 << 20
	req, _ := http.NewRequest("GET", dl, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	t0 = time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("descarga: %v", err)
	}
	got, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	secs := time.Since(t0).Seconds()
	log.Printf("✔ Descarga directa Go: HTTP %d, %d MB en %.2fs = %.0f Mbps", resp.StatusCode, got>>20, secs, float64(got)*8/1e6/secs)

	// 6. Subida directa desde Go con las cookies de la sesión del navegador
	ck, err := chromedp.Call(ctx, network.GetCookies, network.GetCookiesParams{URLs: []string{portal + "/", "https://upload.micloud.movistar.es/"}})
	if err != nil {
		log.Fatalf("cookies: %v", err)
	}
	cookies := ck.Cookies
	var vk string
	var cookieHdr []string
	for _, c := range cookies {
		cookieHdr = append(cookieHdr, c.Name+"="+c.Value)
		if c.Name == "validationKey" {
			vk, _ = url.QueryUnescape(c.Value)
		}
	}
	upName := fmt.Sprintf("zz-webprobe-%d.txt", time.Now().Unix())
	content := []byte("Archivo de prueba de webprobe. Se borra al terminar.\n")
	meta, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{
		"name": upName, "size": len(content), "modificationdate": "20260115T102030Z",
		"contenttype": "text/plain", "folderid": rootID,
	}})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("data", string(meta))
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, upName))
	h.Set("Content-Type", "text/plain")
	fw, _ := mw.CreatePart(h)
	_, _ = fw.Write(content)
	_ = mw.Close()
	req, _ = http.NewRequest("POST", "https://upload.micloud.movistar.es/sapi/upload?action=save&validationkey="+url.QueryEscape(vk), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Cookie", strings.Join(cookieHdr, "; "))
	req.Header.Set("X-deviceid", "web-"+b.deviceID)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Origin", portal)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("subida: %v", err)
	}
	upBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	log.Printf("Subida directa Go con cookies: HTTP %d %s", resp.StatusCode, trunc(string(upBody)))
	var up struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(upBody, &up)

	// 7. Comprobar fecha y borrar (a la papelera) desde el navegador
	if up.ID != "" {
		r, err = b.sapi("POST", "/sapi/media?action=get&origin=omh,dropbox",
			map[string]interface{}{"data": map[string]interface{}{"fields": []string{"name", "modificationdate"}, "ids": []string{up.ID}}})
		if err == nil {
			log.Printf("Metadatos del subido: %s", trunc(r.Body))
		}
		r, err = b.sapi("POST", "/sapi/media/file?action=delete&softdelete=true",
			map[string]interface{}{"data": map[string]interface{}{"files": []interface{}{json.Number(up.ID)}}})
		must("borrar prueba", r, err)
		log.Printf("✔ Archivo de prueba enviado a la papelera")
	}

	// 8. Ráfaga: 40 listados seguidos como el escenario que dispara el WAF al proxy
	t0 = time.Now()
	fails := 0
	for i := 0; i < 40; i++ {
		r, err := b.sapi("GET", fmt.Sprintf("/sapi/media/folder?action=list&parentid=%d&limit=200", rootID), nil)
		if err != nil || r.Status != 200 {
			fails++
			log.Printf("  ráfaga %d: status %d %s", i+1, r.Status, trunc(r.Body))
		}
	}
	log.Printf("✔ Ráfaga: 40 listados en %s, %d fallos", time.Since(t0).Round(time.Millisecond), fails)
}

// runRemoteLogin sirve la vista de inicio de sesión hasta que SAPI reconoce la sesión.
func runRemoteLogin(ctx context.Context, b *browser, addr, profile string) {
	tok := make([]byte, 12)
	_, _ = rand.Read(tok)
	prefix := "/" + hex.EncodeToString(tok) + "/"

	var mu sync.Mutex
	logged := false
	check := func() bool {
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](sapiJS+";true")); err != nil {
			return false
		}
		r, err := b.sapi("GET", "/sapi/profile/role?action=get", nil)
		return err == nil && r.Status == 200
	}

	view := webclient.NewLoginView(ctx)
	view.LoggedIn = func() bool { mu.Lock(); defer mu.Unlock(); return logged }
	srv := &http.Server{Addr: addr, Handler: view.Handler(prefix)}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("servidor de login: %v", err)
		}
	}()
	log.Printf("Abre http://<IP-del-servidor>%s%s para iniciar sesión (10 min)", addr, prefix)

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if check() {
			mu.Lock()
			logged = true
			mu.Unlock()
			log.Println("✔ Sesión iniciada. Perfil guardado en", profile)
			time.Sleep(4 * time.Second) // que la página muestre el estado antes de cerrar
			_ = srv.Close()
			return
		}
	}
	log.Fatal("no se detectó la sesión a tiempo")
}

// closeBrowser cierra Chromium de forma ordenada y espera a que salga, para que
// vuelque las cookies de sesión a disco (las escribe con retraso). Sin esto, al
// terminar el proceso o el contenedor la sesión recién iniciada se pierde.
func closeBrowser(ctx context.Context) {
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := chromedp.Cancel(tctx); err != nil {
		log.Printf("aviso: cierre de Chromium: %v", err)
	}
}

func must(what string, r sapiResp, err error) {
	if err != nil {
		log.Fatalf("%s: %v", what, err)
	}
	if r.Status != 200 {
		log.Fatalf("%s: HTTP %d %s", what, r.Status, trunc(r.Body))
	}
}

func trunc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
