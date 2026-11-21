#!/usr/bin/env python3
"""Pantalla de estado de la nube para la consola de la Raspberry Pi.

Bloques: sesión de Movistar Cloud (izquierda), sistema (derecha), actividad
actual (debajo) y registros del proxy (lo que sobre). Lee el panel del proxy en
http://127.0.0.1:5000 con el usuario del panel (archivo de credenciales en
~/.config/estado-nube/panel.env: WEBUI_USER y WEBUI_PASSWORD). Sin dependencias.
"""
import base64
import curses
import json
import os
import shutil
import socket
import subprocess
import time
import urllib.request
from datetime import datetime, timezone

PANEL = os.environ.get("PANEL_URL", "http://127.0.0.1:5000")
CRED_FILE = os.path.expanduser("~/.config/estado-nube/panel.env")
MOUNT = "/mnt/emby_cloud"
REFRESH = 2.0


def load_creds():
    user = pw = ""
    try:
        with open(CRED_FILE) as f:
            for line in f:
                k, _, v = line.strip().partition("=")
                if k == "WEBUI_USER":
                    user = v
                elif k == "WEBUI_PASSWORD":
                    pw = v
    except OSError:
        pass
    return user, pw


AUTH = None


def api(path, timeout=3):
    global AUTH
    if AUTH is None:
        u, p = load_creds()
        AUTH = "Basic " + base64.b64encode(f"{u}:{p}".encode()).decode()
    req = urllib.request.Request(PANEL + path, headers={"Authorization": AUTH})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode("utf-8", "replace"))
    except Exception:
        return None


# --- sistema -------------------------------------------------------------

_prev_cpu = None


def cpu_percent():
    global _prev_cpu
    try:
        with open("/proc/stat") as f:
            v = [int(x) for x in f.readline().split()[1:]]
    except OSError:
        return None
    idle, total = v[3] + v[4], sum(v)
    pct = None
    if _prev_cpu:
        di, dt = idle - _prev_cpu[0], total - _prev_cpu[1]
        if dt > 0:
            pct = 100.0 * (1 - di / dt)
    _prev_cpu = (idle, total)
    return pct


def meminfo():
    m = {}
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                k, v = line.split(":", 1)
                m[k] = int(v.split()[0]) * 1024
    except OSError:
        pass
    return m


def cpu_temp():
    try:
        with open("/sys/class/thermal/thermal_zone0/temp") as f:
            return int(f.read().strip()) / 1000.0
    except (OSError, ValueError):
        return None


def throttled():
    try:
        out = subprocess.run(["vcgencmd", "get_throttled"], capture_output=True, text=True, timeout=2).stdout
        return int(out.strip().split("=")[1], 16)
    except Exception:
        return None


def uptime():
    try:
        with open("/proc/uptime") as f:
            s = int(float(f.read().split()[0]))
    except OSError:
        return "?"
    d, s = divmod(s, 86400)
    h, s = divmod(s, 3600)
    return (f"{d} d " if d else "") + f"{h} h {s // 60} min"


def mounted(path):
    try:
        with open("/proc/mounts") as f:
            return any(line.split()[1] == path for line in f)
    except OSError:
        return False


def smb_clients():
    try:
        out = subprocess.run(["ss", "-Htn", "state", "established", "( sport = :445 )"], capture_output=True, text=True, timeout=2).stdout
        return len({l.split()[-1].rsplit(":", 1)[0] for l in out.splitlines() if l.strip()})
    except Exception:
        return None


def net_status():
    """Conexión que usa la Pi: la ruta por defecto de menor métrica (cable o Wi-Fi)."""
    best = None
    try:
        with open("/proc/net/route") as f:
            for line in f.readlines()[1:]:
                p = line.split()
                if len(p) > 6 and p[1] == "00000000" and int(p[3], 16) & 1:  # por defecto y activa
                    metric = int(p[6])
                    if best is None or metric < best[1]:
                        best = (p[0], metric)
    except OSError:
        return None, ""
    if best is None:
        return None, "sin conexión"
    dev = best[0]
    if dev.startswith("wl"):
        sig = ""
        try:
            with open("/proc/net/wireless") as f:
                for line in f:
                    if line.strip().startswith(dev + ":"):
                        sig = f"señal {float(line.split()[3].rstrip('.')):.0f} dBm"
        except (OSError, ValueError, IndexError):
            pass
        return "wifi", sig
    speed = ""
    try:
        with open(f"/sys/class/net/{dev}/speed") as f:
            mbps = int(f.read().strip())
            speed = f"{mbps // 1000} Gbit/s" if mbps >= 1000 else f"{mbps} Mbit/s"
    except (OSError, ValueError):
        pass
    return "cable", speed


def wifi_status():
    """Enlace Wi-Fi (aunque esté en reserva): señal en dBm, velocidades y banda, de «iw»."""
    try:
        out = subprocess.run(["/usr/sbin/iw", "dev", "wlan0", "link"], capture_output=True, text=True, timeout=2).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    if "Not connected" in out or "signal" not in out:
        return {}
    info = {}
    for line in out.splitlines():
        k, _, v = line.strip().partition(":")
        v = v.strip()
        try:
            if k == "signal":
                info["signal"] = int(float(v.split()[0]))
            elif k in ("rx bitrate", "tx bitrate"):
                info[k.split()[0]] = float(v.split()[0])
            elif k == "freq":
                info["ghz"] = float(v.split()[0]) / 1000
        except (ValueError, IndexError):
            pass
    return info


def human(n, per_s=False):
    if n is None:
        return "?"
    units = ["B", "KB", "MB", "GB", "TB"]
    n = float(n)
    i = 0
    while n >= 1024 and i < len(units) - 1:
        n /= 1024
        i += 1
    s = f"{n:.1f} {units[i]}" if i else f"{int(n)} {units[i]}"
    return s + "/s" if per_s else s


def left_until(iso):
    try:
        t = datetime.fromisoformat(iso)
    except (TypeError, ValueError):
        return None, None
    d = t - datetime.now(timezone.utc).astimezone(t.tzinfo)
    return t, d.total_seconds()


def fmt_dur(sec):
    if sec is None:
        return "?"
    neg = sec < 0
    sec = abs(int(sec))
    h, m = divmod(sec // 60, 60)
    s = (f"{h} h " if h else "") + f"{m} min"
    return ("hace " + s) if neg else s


# --- dibujo --------------------------------------------------------------

C_OK, C_WARN, C_BAD, C_TITLE, C_DIM, C_ACC = range(1, 7)


def init_colors():
    curses.start_color()
    try:
        curses.use_default_colors()
        bg = -1
    except curses.error:
        bg = curses.COLOR_BLACK
    curses.init_pair(C_OK, curses.COLOR_GREEN, bg)
    curses.init_pair(C_WARN, curses.COLOR_YELLOW, bg)
    curses.init_pair(C_BAD, curses.COLOR_RED, bg)
    curses.init_pair(C_TITLE, curses.COLOR_CYAN, bg)
    curses.init_pair(C_DIM, curses.COLOR_WHITE, bg)
    curses.init_pair(C_ACC, curses.COLOR_MAGENTA, bg)


def put(win, y, x, text, attr=0, maxw=None):
    h, w = win.getmaxyx()
    if y < 0 or y >= h or x >= w:
        return
    avail = (w - x - 1) if maxw is None else min(maxw, w - x - 1)
    if avail <= 0:
        return
    try:
        win.addnstr(y, x, text, avail, attr)
    except curses.error:
        pass


def box(win, y, x, h, w, title):
    if h < 3 or w < 6:
        return
    try:
        sub = win.derwin(h, w, y, x)
        sub.attrset(curses.color_pair(C_DIM))
        sub.box()
        sub.attrset(0)
        put(sub, 0, 2, f" {title} ", curses.color_pair(C_TITLE) | curses.A_BOLD)
    except curses.error:
        pass


def bar(pct, width):
    width = max(width, 4)
    filled = int(round(width * max(0.0, min(100.0, pct or 0)) / 100))
    return "█" * filled + "░" * (width - filled)


def level_color(v, warn, bad):
    if v is None:
        return curses.color_pair(C_DIM)
    if v >= bad:
        return curses.color_pair(C_BAD) | curses.A_BOLD
    if v >= warn:
        return curses.color_pair(C_WARN) | curses.A_BOLD
    return curses.color_pair(C_OK) | curses.A_BOLD


def row(win, y, x, w, label, value, attr=0):
    put(win, y, x, f"{label:<14}", curses.color_pair(C_DIM))
    put(win, y, x + 14, value, attr, w - 14)


def draw_session(win, y, x, h, w, sess, cstat, storage, crypt):
    box(win, y, x, h, w, "Sesión de Movistar Cloud")
    cx, cy, cw = x + 2, y + 1, w - 4
    if sess is None:
        row(win, cy, cx, cw, "Proxy", "sin respuesta del panel", curses.color_pair(C_BAD) | curses.A_BOLD)
        return
    ok = sess.get("logged_in")
    row(win, cy, cx, cw, "Estado", "● Activa" if ok else "● NO iniciada", curses.color_pair(C_OK if ok else C_BAD) | curses.A_BOLD)
    t, left = left_until(sess.get("expires"))
    if t:
        col = C_OK if left and left > 3 * 3600 else (C_WARN if left and left > 0 else C_BAD)
        row(win, cy + 1, cx, cw, "Caduca", f"{t.strftime('%d/%m %H:%M')}  ({fmt_dur(left)})", curses.color_pair(col))
    rem = sess.get("remember")
    row(win, cy + 2, cx, cw, "Renovación", "automática" if rem else "manual (sin «No cerrar sesión»)", curses.color_pair(C_OK if rem else C_WARN))
    if storage:
        row(win, cy + 3, cx, cw, "Usado", f"{storage.get('used_str', '?')}  ({'ilimitado' if storage.get('nolimit') else storage.get('total_str', '')})")
    m = mounted(MOUNT)
    row(win, cy + 4, cx, cw, "Montaje", f"● {MOUNT}" if m else "● sin montar", curses.color_pair(C_OK if m else C_BAD))
    n = smb_clients()
    row(win, cy + 5, cx, cw, "Samba", "?" if n is None else (f"{n} equipo(s) conectado(s)" if n else "sin conexiones"))
    ac = (crypt or {}).get("autochunk") or {}
    chunk = ac.get("chunk") or 0
    row(win, cy + 6, cx, cw, "Trozo", f"{chunk >> 20} MiB (rebajado por Movistar)" if chunk else "el de la configuración",
        curses.color_pair(C_WARN if chunk else C_OK))
    if cstat is not None:
        row(win, cy + 7, cx, cw, "Límite subida", cstat.get("bwlimit") or "off")
    via, detail = net_status()
    wf = wifi_status()
    if wf is not None:
        if not wf:
            row(win, cy + 9, cx, cw, "Wi-Fi", "● desconectado", curses.color_pair(C_BAD))
        else:
            sig = wf.get("signal")
            col = C_OK if sig is not None and sig >= -60 else (C_WARN if sig is not None and sig >= -70 else C_BAD)
            # Barritas de cobertura: las encendidas en el color de la señal, el resto apagadas.
            lit = 0 if sig is None else 4 if sig >= -55 else 3 if sig >= -65 else 2 if sig >= -75 else 1
            put(win, cy + 9, cx, f"{'Wi-Fi':<14}", curses.color_pair(C_DIM))
            for i, ch in enumerate("▂▄▆█"):
                put(win, cy + 9, cx + 14 + i, ch, curses.color_pair(col) | curses.A_BOLD if i < lit else curses.color_pair(C_DIM) | curses.A_DIM)
            estado = "en uso" if via == "wifi" else "reserva"
            enlace = f"{max(wf.get('rx', 0), wf.get('tx', 0)):.0f} Mb/s"
            put(win, cy + 9, cx + 20, f"{estado} · {enlace}", curses.color_pair(C_OK if via == "wifi" else C_DIM), cw - 20)
    if via == "cable":
        row(win, cy + 8, cx, cw, "Conexión", f"● Cable  {detail}".rstrip(), curses.color_pair(C_OK))
    elif via == "wifi":
        row(win, cy + 8, cx, cw, "Conexión", f"● Wi-Fi (sin cable)  {detail}".rstrip(), curses.color_pair(C_WARN) | curses.A_BOLD)
    else:
        row(win, cy + 8, cx, cw, "Conexión", f"● {detail or '?'}", curses.color_pair(C_BAD) | curses.A_BOLD)


def draw_system(win, y, x, h, w):
    box(win, y, x, h, w, "Raspberry Pi")
    cx, cy, cw = x + 2, y + 1, w - 4
    bw = max(cw - 14 - 8, 6)
    cpu = cpu_percent()
    row(win, cy, cx, cw, "CPU", "")
    put(win, cy, cx + 14, bar(cpu, bw), level_color(cpu, 70, 90))
    put(win, cy, cx + 15 + bw, f"{cpu:5.1f}%" if cpu is not None else "  ...", level_color(cpu, 70, 90))
    m = meminfo()
    tot, av = m.get("MemTotal", 0), m.get("MemAvailable", 0)
    used = tot - av
    mp = 100.0 * used / tot if tot else None
    row(win, cy + 1, cx, cw, "Memoria", "")
    put(win, cy + 1, cx + 14, bar(mp, bw), level_color(mp, 75, 90))
    put(win, cy + 1, cx + 15 + bw, f"{mp:5.1f}%" if mp is not None else "", level_color(mp, 75, 90))
    put(win, cy + 2, cx + 14, f"{human(used)} de {human(tot)}", curses.color_pair(C_DIM))
    st, sf = m.get("SwapTotal", 0), m.get("SwapFree", 0)
    if st:
        row(win, cy + 3, cx, cw, "Swap", f"{human(st - sf)} de {human(st)}", level_color(100.0 * (st - sf) / st, 30, 70))
    t = cpu_temp()
    row(win, cy + 4, cx, cw, "Temperatura", f"{t:.1f} °C" if t is not None else "?", level_color(t, 65, 78))
    th = throttled()
    if th is not None:
        now = th & 0xF
        if now & 0x4 or now & 0x2:
            txt, col = "LIMITANDO AHORA", C_BAD
        elif now & 0x8:
            txt, col = "límite suave de temperatura", C_WARN
        elif th & 0xF0000:
            txt, col = "ha limitado desde que arrancó", C_WARN
        else:
            txt, col = "sin limitar", C_OK
        if now & 0x1:
            txt, col = "TENSIÓN BAJA", C_BAD
        row(win, cy + 5, cx, cw, "Velocidad", txt, curses.color_pair(col))
    l1, l5, l15 = os.getloadavg()
    row(win, cy + 6, cx, cw, "Carga", f"{l1:.2f}  {l5:.2f}  {l15:.2f}")
    du = shutil.disk_usage("/")
    row(win, cy + 7, cx, cw, "Disco", f"{human(du.used)} de {human(du.total)}")
    row(win, cy + 8, cx, cw, "Encendida", uptime())


def transfer_line(t, w):
    name = t.get("name") or "?"
    pct = t.get("percentage") or 0
    sp = t.get("speed") or 0
    right = f" {pct:3d}%  {human(sp, True):>10}"
    nw = max(w - len(right) - 12, 10)
    if len(name) > nw:
        name = "…" + name[-(nw - 1):]
    return name, pct, right, nw


def draw_copy(win, y, x, h, w, cstat):
    """Progreso general de la copia que manda el rclone del cliente (Enviar a > Nube)."""
    c = cstat or {}
    box(win, y, x, h, w, "Copia en curso (rclone)")
    cx, cy, cw = x + 2, y + 1, w - 4
    cur = c.get("current")
    if not cur:
        put(win, cy, cx, "Sin copia en curso.", curses.color_pair(C_DIM))
        return
    try:
        pct = int(c.get("percent") or 0)
    except ValueError:
        pct = 0
    put(win, cy, cx, cur, curses.A_BOLD, cw)
    right = f" {pct:3d}%"
    bw = max(cw - len(right) - 1, 6)
    put(win, cy + 1, cx, bar(pct, bw), curses.color_pair(C_ACC))
    put(win, cy + 1, cx + bw, right, curses.color_pair(C_ACC) | curses.A_BOLD)
    parts = []
    if c.get("transferred"):
        parts.append(f"{c.get('transferred')} de {c.get('total', '?')}")
    if c.get("speed"):
        parts.append(c["speed"])
    if c.get("eta"):
        parts.append(f"quedan {c['eta']}")
    errs = c.get("error_count") or 0
    parts.append(f"{errs} errores")
    put(win, cy + 2, cx, "   ·   ".join(parts), curses.color_pair(C_BAD if errs else C_DIM), cw)


def draw_activity(win, y, x, h, w, cstat):
    up = (cstat or {}).get("uploading") or []
    rd = (cstat or {}).get("reading") or []
    q = (cstat or {}).get("queued") or []
    us, rs = (cstat or {}).get("upload_speed") or 0, (cstat or {}).get("read_speed") or 0
    box(win, y, x, h, w, f"Actividad  ↑ {human(us, True)}   ↓ {human(rs, True)}")
    cx, cy, cw = x + 2, y + 1, w - 4
    lines = [("↑", C_ACC, t) for t in up] + [("↓", C_TITLE, t) for t in rd] + [("⏳", C_WARN, t) for t in q]
    if not lines:
        put(win, cy, cx, "Sin actividad: no se está subiendo ni leyendo nada.", curses.color_pair(C_DIM))
        return
    for i, (sym, col, t) in enumerate(lines[: h - 2]):
        name, pct, right, nw = transfer_line(t, cw)
        put(win, cy + i, cx, sym, curses.color_pair(col) | curses.A_BOLD)
        put(win, cy + i, cx + 2, name, 0, nw)
        put(win, cy + i, cx + 3 + nw, bar(pct, 8), curses.color_pair(col))
        put(win, cy + i, cx + 12 + nw, right, curses.color_pair(col))
    if len(lines) > h - 2:
        put(win, y + h - 1, x + w - 16, f" +{len(lines) - (h - 2)} más ", curses.color_pair(C_DIM))


def draw_logs(win, y, x, h, w, logs):
    box(win, y, x, h, w, "Registros del proxy")
    lines = (logs or {}).get("logs") or []
    cx, cw = x + 2, w - 4
    show = lines[-(h - 2):] if h > 2 else []
    for i, line in enumerate(show):
        col = 0
        if "❌" in line or "ERROR" in line:
            col = curses.color_pair(C_BAD)
        elif "⚠" in line:
            col = curses.color_pair(C_WARN)
        elif "✔" in line:
            col = curses.color_pair(C_OK)
        put(win, y + 1 + i, cx, line, col, cw)


def main(stdscr):
    curses.curs_set(0)
    init_colors()
    stdscr.timeout(int(REFRESH * 1000))
    cpu_percent()
    host = socket.gethostname()
    tick = 0
    sess = storage = crypt = logs = None
    while True:
        cstat = api("/copy_status")
        if tick % 5 == 0:  # lo que cambia poco, cada ~10 s
            sess = api("/web_session")
            storage = api("/storage_info")
            crypt = api("/crypt_status")
        logs = api("/logs")
        tick += 1

        # Redibujo completo cada ~10 s: si algo escribe en la consola (mensajes del
        # sistema), la pantalla se desplaza y curses solo repintaría lo que cambia.
        if tick % 5 == 1:
            stdscr.clear()
        else:
            stdscr.erase()
        H, W = stdscr.getmaxyx()
        put(stdscr, 0, 1, f" Nube de Emby · {host} ", curses.color_pair(C_TITLE) | curses.A_BOLD | curses.A_REVERSE)
        put(stdscr, 0, max(W - 22, 1), datetime.now().strftime("%d/%m/%Y %H:%M:%S"), curses.color_pair(C_DIM))
        top = 1
        top_h = 12
        if W >= 90:
            lw = W // 2
            draw_session(stdscr, top, 0, top_h, lw, sess, cstat, storage, crypt)
            draw_system(stdscr, top, lw, top_h, W - lw)
            y = top + top_h
        else:
            draw_session(stdscr, top, 0, top_h, W, sess, cstat, storage, crypt)
            draw_system(stdscr, top + top_h, 0, top_h, W)
            y = top + 2 * top_h
        copy_h = 5
        if H - y >= copy_h + 6:
            draw_copy(stdscr, y, 0, copy_h, W, cstat)
            y += copy_h
        act_h = max(min(8, H - y - 3), 3)
        draw_activity(stdscr, y, 0, act_h, W, cstat)
        y += act_h
        if H - y >= 4:
            draw_logs(stdscr, y, 0, H - y, W, logs)
        stdscr.refresh()
        if stdscr.getch() in (ord("q"), ord("Q")):
            break


if __name__ == "__main__":
    os.environ.setdefault("ESCDELAY", "25")
    curses.wrapper(main)
