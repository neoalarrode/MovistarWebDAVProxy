package main

import (
	"regexp"
	"strconv"
	"strings"

	"movistar-webdav-proxy/internal/rcloneconf"
)

// Tamaños de los objetos que escribe la cadena de rclone de una carpeta (crypt y/o
// chunker) para un archivo de tamaño conocido, con las mismas fórmulas que rclone.

const (
	cryptHeaderSize    = 32    // «RCLONE\x00\x00» + nonce
	cryptBlockData     = 65536 // datos por bloque
	cryptBlockOverhead = 16    // Poly1305 por bloque
)

// cryptEncryptedSize es rclone crypt EncryptedSize.
func cryptEncryptedSize(size int64) int64 {
	blocks, residue := size/cryptBlockData, size%cryptBlockData
	n := cryptHeaderSize + blocks*(cryptBlockOverhead+cryptBlockData)
	if residue != 0 {
		n += cryptBlockOverhead + residue
	}
	return n
}

// uploadSizer calcula, para un archivo de tamaño size subido a la carpeta, el
// tamaño de cada objeto en bruto por su nombre. Devuelve nil si la cadena no es de
// las que sabe calcular (crypt sobre chunker, crypt, chunker o nada).
type uploadSizer struct {
	crypt     bool
	chunkSize int64
	chunkRe   *regexp.Regexp // nombre de trozo: grupo 1 = base, grupo 2 = número
	startFrom int
}

func newUploadSizer(ch *rcloneconf.Chain) *uploadSizer {
	u := &uploadSizer{}
	if ch == nil {
		return u
	}
	for i, t := range ch.Types {
		switch t {
		case "crypt":
			// crypt tiene que ir arriba del todo (cifra antes de trocear).
			if i != 0 {
				return nil
			}
			u.crypt = true
		case "chunker":
			s := ch.Sections[i]
			cs := s.Get("chunk_size")
			if cs == "" {
				cs = "2Gi" // valor por defecto de rclone chunker
			}
			n, err := rcloneconf.ParseSize(cs)
			if err != nil {
				return nil
			}
			nf := s.Get("name_format")
			if nf == "" {
				nf = "*.rclone_chunk.###"
			}
			if strings.Count(nf, "*") != 1 || !strings.Contains(nf, "#") {
				return nil
			}
			pat := regexp.QuoteMeta(nf)
			pat = strings.Replace(pat, `\*`, `(.+)`, 1)
			pat = regexp.MustCompile(`#+`).ReplaceAllString(pat, `([0-9]+)`)
			// Sufijo temporal de las transacciones del chunker («_xxxx» o «..tmp_…»).
			u.chunkRe = regexp.MustCompile(`^` + pat + `(?:_[0-9a-z]{4,9}|\.\.tmp_[0-9]+)?$`)
			u.chunkSize = n
			u.startFrom = 1
			if v := s.Get("start_from"); v != "" {
				if sf, err := strconv.Atoi(v); err == nil {
					u.startFrom = sf
				}
			}
		default:
			return nil
		}
	}
	return u
}

// sizeOf devuelve el tamaño del objeto en bruto rawName (último segmento) para un
// archivo de tamaño size cuyo nombre en bruto es base; -1 si no se sabe.
func (u *uploadSizer) sizeOf(size int64, base, rawName string) int64 {
	total := size
	if u.crypt {
		total = cryptEncryptedSize(size)
	}
	if u.chunkRe == nil {
		if rawName == base {
			return total
		}
		return -1
	}
	if rawName == base {
		// Archivo de un solo trozo: se guarda con el nombre tal cual. Si tiene varios,
		// ese nombre es el objeto de metadatos del chunker (no se calcula).
		if total <= u.chunkSize {
			return total
		}
		return -1
	}
	m := u.chunkRe.FindStringSubmatch(rawName)
	if m == nil || m[1] != base {
		return -1 // p. ej. el objeto de metadatos del chunker
	}
	idx, err := strconv.Atoi(m[2])
	if err != nil {
		return -1
	}
	off := int64(idx-u.startFrom) * u.chunkSize
	if off < 0 || off >= total && !(total == 0 && off == 0) {
		return -1
	}
	if rest := total - off; rest < u.chunkSize {
		return rest
	}
	return u.chunkSize
}
