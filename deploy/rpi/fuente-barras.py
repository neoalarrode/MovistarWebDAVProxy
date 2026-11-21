#!/usr/bin/env python3
# Copia de la fuente de consola (PSF1, 8x16) con las barritas de cobertura ▂▄▆:
# reutiliza 3 dibujos de letras cirílicas que no se usan. Lo demás queda igual.
import gzip, struct, sys
src, dst = sys.argv[1], sys.argv[2]
d = gzip.open(src).read()
assert d[:2] == b"\x36\x04", "no es PSF1"
mode, cs = d[2], d[3]
n = 512 if mode & 1 else 256
glyphs = [bytearray(d[4 + i * cs:4 + (i + 1) * cs]) for i in range(n)]
raw = d[4 + n * cs:]
vals = [struct.unpack("<H", raw[i:i + 2])[0] for i in range(0, len(raw) - 1, 2)]
# Tabla unicode: por cada dibujo, sus códigos hasta 0xFFFF (las secuencias tras 0xFFFE se conservan).
table, cur = [], []
for v in vals:
    if v == 0xFFFF:
        table.append(cur); cur = []
    else:
        cur.append(v)
assert len(table) == n, (len(table), n)
# Dibujos cuyos códigos son todos cirílicos (U+0400–U+04FF) y sin secuencias: candidatos.
cand = [i for i, codes in enumerate(table) if codes and 0xFFFE not in codes and all(0x0400 <= c <= 0x04FF for c in codes)]
assert len(cand) >= 3, "no hay huecos"
heights = {0x2582: 4, 0x2584: 8, 0x2586: 12}  # filas encendidas desde abajo (de 16)
for slot, (code, h) in zip(cand[-3:], heights.items()):
    g = bytearray(cs)
    for row in range(cs - h, cs):
        g[row] = 0xFF
    glyphs[slot] = g
    table[slot] = [code]
out = bytearray(d[:4])
for g in glyphs:
    out += g
for codes in table:
    for c in codes:
        out += struct.pack("<H", c)
    out += struct.pack("<H", 0xFFFF)
with gzip.open(dst, "wb") as f:
    f.write(bytes(out))
print("barritas en los dibujos", cand[-3:])
