package webui

import (
	"testing"

	"movistar-webdav-proxy/internal/webdav"
)

// Sin copia del script, la cabecera enseña el total que manda el cliente.
func TestApplyClientCopies(t *testing.T) {
	eta := int64(120)
	webdav.ReportClientCopy(webdav.ClientCopy{ID: "t-1", Dest: "Disney+/Serie", Bytes: 1 << 30, TotalBytes: 4 << 30, Speed: 20 << 20, ETA: &eta, Errors: 1})
	t.Cleanup(func() { webdav.ReportClientCopy(webdav.ClientCopy{ID: "t-1", Done: true}) })
	var st copyStatus
	applyClientCopies(&st)
	if st.Current != "Disney+/Serie" || st.Percent != "25" || st.ETA == "" || st.ErrorCount != 1 || st.Total == "" {
		t.Fatalf("cabecera: %+v", st)
	}
	st = copyStatus{Current: "Peliculas"}
	applyClientCopies(&st)
	if st.Current != "Peliculas" {
		t.Fatal("no debe pisar la copia del script")
	}
}
