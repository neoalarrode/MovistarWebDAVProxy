package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Carpetas de primer nivel de la biblioteca, con su actividad en vivo. Las detecta
// un servicio del host (folders.json: listado de /mnt/emby_merged y si cada una
// existe ya en la nube); si no hay volcado, se usa plan.txt.
//
//	folders.json     {"folders":[{"name":"Peliculas","in_cloud":true}, ...]}
//	vfs-queue.json   "rclone rc vfs/queue" del montaje: archivos en la caché de escritura
//	origen-danado.txt  "<carpeta>\t<ruta>\t..." archivos ilegibles en el NAS

type folderStatus struct {
	Name     string `json:"name"`
	InCloud  bool   `json:"in_cloud"`
	Copy     string `json:"copy"` // done | done_warn | running | failed | pending | excluded | none
	Damaged  int    `json:"damaged"`
	Uploads  int    `json:"uploads"`
	Reads    int    `json:"reads"`
	Cached   int    `json:"cached"`
	Activity string `json:"activity"` // upload | download | cache | idle
}

type vfsQueueItem struct {
	Name      string `json:"name"`
	Uploading bool   `json:"uploading"`
}

// topFolder devuelve el primer componente de una ruta relativa ("A/b/c.mkv" → "A").
func topFolder(p string) string {
	p = strings.TrimLeft(filepath.ToSlash(p), "/")
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

func readFreshJSON(path string, staleAfter time.Duration, dst interface{}) bool {
	fi, err := os.Stat(path)
	if err != nil || (staleAfter > 0 && time.Since(fi.ModTime()) > staleAfter) {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, dst) == nil
}

func buildFolders(dir string, st copyStatus, copyUploads int) []folderStatus {
	var detected struct {
		Folders []struct {
			Name    string `json:"name"`
			InCloud bool   `json:"in_cloud"`
		} `json:"folders"`
	}
	byName := map[string]*folderStatus{}
	var order []string
	add := func(name string) *folderStatus {
		if f, ok := byName[name]; ok {
			return f
		}
		byName[name] = &folderStatus{Name: name, Copy: "none"}
		order = append(order, name)
		return byName[name]
	}
	autodetected := readFreshJSON(filepath.Join(dir, "folders.json"), 10*time.Minute, &detected)
	if autodetected {
		for _, f := range detected.Folders {
			add(f.Name).InCloud = f.InCloud
		}
	} else {
		for _, l := range st.Libraries {
			add(l.Name)
		}
	}

	// Estado de la copia de cada carpeta. Las que no están en el plan (copias de
	// seguridad de Emby, etc.) se muestran igual, marcadas como fuera de la copia.
	if len(st.Libraries) > 0 {
		for _, f := range byName {
			f.Copy = "excluded"
		}
	}
	for _, l := range st.Libraries {
		f, ok := byName[l.Name]
		if !ok {
			continue // carpeta del plan que ya no existe
		}
		f.Copy = l.State
	}
	for _, line := range readLines(filepath.Join(dir, "origen-danado.txt")) {
		if name := strings.SplitN(line, "\t", 2)[0]; byName[name] != nil {
			byName[name].Damaged++
		}
	}
	for _, f := range byName {
		// Terminada con error solo por archivos ilegibles en el origen: aviso, no fallo.
		if f.Copy == "failed" && f.Damaged > 0 {
			f.Copy = "done_warn"
		}
	}

	// Actividad en vivo.
	if st.Current != "" && byName[st.Current] != nil {
		byName[st.Current].Uploads += copyUploads // la copia sube desde la raíz de la carpeta
	}
	for _, q := range readVFSQueue(dir) {
		f := byName[topFolder(q.Name)]
		if f == nil {
			continue
		}
		if q.Uploading {
			f.Uploads++
		} else {
			f.Cached++
		}
	}
	for _, r := range st.Reading {
		if f := byName[topFolder(r.Name)]; f != nil {
			f.Reads++
		}
	}

	out := make([]folderStatus, 0, len(order))
	for _, name := range order {
		f := byName[name]
		switch {
		case f.Uploads > 0:
			f.Activity = "upload"
		case f.Reads > 0:
			f.Activity = "download"
		case f.Cached > 0:
			f.Activity = "cache"
		default:
			f.Activity = "idle"
		}
		out = append(out, *f)
	}
	if !autodetected {
		return out // sin detección se respeta el orden del plan
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func readVFSQueue(dir string) []vfsQueueItem {
	var queue struct {
		Queue []vfsQueueItem `json:"queue"`
	}
	if !readFreshJSON(filepath.Join(dir, "vfs-queue.json"), 90*time.Second, &queue) {
		return nil
	}
	return queue.Queue
}

// separateMountUploads saca de las lecturas del montaje los archivos que en
// realidad se están subiendo desde la caché de escritura y los pasa a subidas.
func separateMountUploads(dir string, st *copyStatus) {
	uploading := map[string]bool{}
	for _, q := range readVFSQueue(dir) {
		uploading[q.Name] = true
	}
	if len(uploading) == 0 {
		return
	}
	reads := st.Reading[:0]
	for _, r := range st.Reading {
		if uploading[r.Name] {
			st.Uploading = append(st.Uploading, r)
		} else {
			reads = append(reads, r)
		}
	}
	st.Reading = reads
}
