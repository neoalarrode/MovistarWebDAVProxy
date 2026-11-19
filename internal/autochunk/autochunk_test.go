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
	// Un MED-1048 suelto no cambia nada; hace falta un segundo.
	if _, ok := Rejected(gi, gi); ok {
		t.Fatal("un solo rechazo no debe bajar el trozo")
	}
	if n, ok := Rejected(gi, gi); !ok || n != 512*mi {
		t.Fatalf("1 GiB rechazado: %d %v", n, ok)
	}
	Rejected(512*mi, 512*mi)
	if n, ok := Rejected(512*mi, 512*mi); !ok || n != 256*mi {
		t.Fatalf("512 MiB rechazado: %d %v", n, ok)
	}
	Accepted(100 * mi) // un archivo de 100 MiB sí pasa
	Rejected(256*mi, 256*mi)
	if n, ok := Rejected(256*mi, 256*mi); !ok || n != 128*mi {
		t.Fatalf("256 MiB rechazado: %d %v", n, ok)
	}
	Rejected(128*mi, 128*mi)
	if n, ok := Rejected(128*mi, 128*mi); !ok || n != 100*mi {
		t.Fatalf("128 MiB rechazado con 100 MiB aceptado: %d %v", n, ok)
	}
	// Un rechazo de algo mayor que el trozo en vigor no lo vuelve a cambiar.
	if _, ok := Rejected(gi, 100*mi); ok {
		t.Fatal("no debe cambiar por un rechazo mayor que el trozo en vigor")
	}
	if Chunk() != 100*mi || changes != 4 {
		t.Fatalf("trozo %d, cambios %d", Chunk(), changes)
	}
	// Persistencia.
	st = State{}
	Load()
	if Chunk() != 100*mi {
		t.Fatalf("no se guardó: %d", Chunk())
	}
	Reset()
	if Chunk() != 0 {
		t.Fatal("reset")
	}
}
