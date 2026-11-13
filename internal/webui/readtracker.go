package webui

import (
	"sort"
	"sync"
	"time"
)

// readTracker recuerda las lecturas recientes desde la nube. Los reproductores leen
// a ráfagas (llenan el búfer y pasan minutos sin pedir nada), así que una
// reproducción en curso desaparecería del volcado de rclone entre ráfaga y ráfaga.
// Se mantiene visible, marcada "en búfer", hasta keep después de la última lectura.
type readTracker struct {
	mu   sync.Mutex
	keep time.Duration
	seen map[string]trackedRead
}

type trackedRead struct {
	t    copyTransfer
	last time.Time
}

func newReadTracker(keep time.Duration) *readTracker {
	return &readTracker{keep: keep, seen: map[string]trackedRead{}}
}

// merge devuelve las lecturas activas más las recientes en búfer.
func (rt *readTracker) merge(active []copyTransfer, now time.Time) []copyTransfer {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	live := map[string]bool{}
	for _, t := range active {
		live[t.Name] = true
		rt.seen[t.Name] = trackedRead{t: t, last: now}
	}
	out := append([]copyTransfer{}, active...)
	for name, r := range rt.seen {
		if live[name] {
			continue
		}
		age := now.Sub(r.last)
		if age > rt.keep {
			delete(rt.seen, name)
			continue
		}
		t := r.t
		t.Idle = true
		t.IdleSeconds = int64(age.Seconds())
		t.Speed, t.ETA = 0, nil
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
