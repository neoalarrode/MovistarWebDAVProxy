package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFolders(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("progress.log", "2026-11-13 08:00:00 INICIO Peliculas\n2026-11-13 10:54:41 FIN Peliculas rc=1\n2026-11-13 10:54:41 INICIO RTVE Play\n")
	// Incluye una línea sin ruta y un duplicado: no deben contar.
	write("origen-danado.txt", "Peliculas\tCigüeñas.mkv\t1\tx\nPeliculas\t\t0\ty\nPeliculas\tCigüeñas.mkv\t1\tz\n")
	write("plan.txt", "Peliculas\nRTVE Play\nNetflix\nMovistar+\nDisney+\n")
	// folders.json es el ls del montaje de la nube: Disney+ (pendiente) aún no está.
	write("folders.json", `{"folders":[{"name":"Peliculas","in_cloud":true},{"name":"RTVE Play","in_cloud":true},{"name":"Netflix","in_cloud":true},{"name":"Movistar+","in_cloud":true},{"name":"DVR","in_cloud":true}]}`)
	write("stats.json", `{"transferring":[{"name":"Serie/T1/e1.mkv","size":10,"bytes":1,"percentage":10}]}`)
	write("mount-stats.json", `{"transferring":[{"name":"Netflix/Dark/e3.mkv","size":10,"bytes":3,"percentage":30},{"name":"Movistar+/nuevo.mkv","size":10,"bytes":2,"percentage":20}]}`)
	write("vfs-queue.json", `{"queue":[{"name":"Movistar+/nuevo.mkv","uploading":true},{"name":"DVR/grab.ts","uploading":false}]}`)

	st := readCopyStatus(dir, "")
	got := map[string]folderStatus{}
	for _, f := range st.Folders {
		got[f.Name] = f
	}
	check := func(name, activity, copy string) {
		t.Helper()
		f, ok := got[name]
		if !ok {
			t.Fatalf("falta la carpeta %q", name)
		}
		if f.Activity != activity || f.Copy != copy {
			t.Errorf("%s: actividad %q y copia %q; esperado %q y %q", name, f.Activity, f.Copy, activity, copy)
		}
	}
	check("RTVE Play", "upload", "running")
	check("Netflix", "download", "pending")
	check("Movistar+", "upload", "pending")
	check("DVR", "cache", "excluded")
	check("Peliculas", "idle", "done_warn")
	if f := got["Peliculas"]; f.Damaged != 1 || len(f.DamagedFiles) != 1 {
		t.Errorf("Peliculas: esperado 1 dañado, hay %d (%q)", f.Damaged, f.DamagedFiles)
	}
	check("Disney+", "idle", "pending")
	if f := got["Disney+"]; f.InCloud {
		t.Error("Disney+ aún no está en la nube")
	}
	if _, ok := got["Emby Server Databases"]; ok {
		t.Error("una carpeta que solo está en el NAS no debe salir")
	}

	if len(st.Reading) != 1 || st.Reading[0].Name != "Netflix/Dark/e3.mkv" {
		t.Errorf("la subida del montaje no debe salir como lectura: %+v", st.Reading)
	}
	if len(st.Uploading) != 2 {
		t.Errorf("esperadas 2 subidas (copia + montaje), hay %d", len(st.Uploading))
	}
}
