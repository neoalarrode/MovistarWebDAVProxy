package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCopyStatus(t *testing.T) {
	dir := t.TempDir()
	ctl := filepath.Join(dir, "control")
	_ = os.Mkdir(ctl, 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plan.txt", "Peliculas\nAMC+\nNetflix\n")
	write("progress.log", "2026-11-12 22:43:38 INICIO Peliculas\n2026-11-13 08:10:00 FIN Peliculas rc=0\n2026-11-13 08:10:01 INICIO AMC+\n")
	write("AMC+.log", "2026/11/13 08:20:00 INFO  :    42.311 GiB / 168.200 GiB, 25%, 31.020 MiB/s, ETA 1h9m\n2026/11/13 08:21:00 ERROR : a.mkv: Failed to copy: 503\n")
	_ = os.WriteFile(filepath.Join(ctl, "bwlimit"), []byte("20M\n"), 0o644)

	st := readCopyStatus(dir, ctl)
	if !st.Enabled || st.Current != "AMC+" {
		t.Fatalf("esperada copia de AMC+ en curso: %+v", st)
	}
	want := map[string]string{"Peliculas": "done", "AMC+": "running", "Netflix": "pending"}
	for _, l := range st.Libraries {
		if want[l.Name] != l.State {
			t.Errorf("%s: estado %q, esperado %q", l.Name, l.State, want[l.Name])
		}
	}
	if st.Percent != "25" || st.Speed != "31.020 MiB/s" || st.ETA != "1h9m" || st.Total != "168.200 GiB" {
		t.Errorf("estadísticas mal leídas: %+v", st)
	}
	if st.ErrorCount != 1 || st.BWLimit != "20M" {
		t.Errorf("errores %d (esperado 1), límite %q (esperado 20M)", st.ErrorCount, st.BWLimit)
	}
}
