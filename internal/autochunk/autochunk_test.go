package autochunk

import (
	"testing"

	"movistar-webdav-proxy/internal/config"
)

// Con un límite de 100 MiB y trozos de 1 GiB, baja a la mitad en cada rechazo y,
// si ya se ha visto pasar algo más grande, se queda en eso.
func TestRejectedStepsDown(t *testing.T) {
	old := config.TokensDir
	config.TokensDir = t.TempDir()
	t.Cleanup(func() { config.TokensDir = old })
	st = State{}
	changes := 0
	OnChange(func() { changes++ })
	t.Cleanup(func() { OnChange(nil) })

	const gi, mi = int64(1 << 30), int64(1 << 20)
	// Los de subidas pequeñas no cuentan.
	Rejected(5*mi, 0)
	Rejected(5*mi, 0)
	if Chunk() != 0 {
		t.Fatal("un MED-1048 de 5 MiB no debe bajar el trozo")
	}
	// Un MED-1048 suelto no cambia nada; hace falta un segundo.
	if _, ok := Rejected(gi, gi); ok {
		t.Fatal("un solo rechazo no debe bajar el trozo")
	}
	// Primer escalón: directamente a 95 MiB (por debajo de los 100 MB de Movistar).
	if n, ok := Rejected(gi, gi); !ok || n != 95*mi {
		t.Fatalf("1 GiB rechazado: %d %v", n, ok)
	}
	// Si 95 MiB también se rechaza, a la mitad; con algo aceptado mayor, a eso.
	Accepted(60 * mi)
	Rejected(95*mi, 95*mi)
	if n, ok := Rejected(95*mi, 95*mi); !ok || n != 60*mi {
		t.Fatalf("95 MiB rechazado con 60 MiB aceptado: %d %v", n, ok)
	}
	// Un rechazo de algo mayor que el trozo en vigor no lo vuelve a cambiar.
	if _, ok := Rejected(gi, 60*mi); ok {
		t.Fatal("no debe cambiar por un rechazo mayor que el trozo en vigor")
	}
	if Chunk() != 60*mi || changes != 2 {
		t.Fatalf("trozo %d, cambios %d", Chunk(), changes)
	}
	// Persistencia.
	st = State{}
	Load()
	if Chunk() != 60*mi {
		t.Fatalf("no se guardó: %d", Chunk())
	}
	Reset()
	if Chunk() != 0 {
		t.Fatal("reset")
	}
}
