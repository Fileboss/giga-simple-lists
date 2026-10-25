package main

import (
	"log/slog"
	"sync"
	"time"
)

// Journal borné : une erreur ou un refus qui se répète à chaque requête (base pleine,
// IP bloquée qui insiste…) ne doit pas remplir le disque. Chaque message n'est écrit
// qu'une fois par minute au plus ; les occurrences sautées sont comptées.
const logEvery = time.Minute

var logThrottle = struct {
	sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
}{last: map[string]time.Time{}, suppressed: map[string]int{}}

func throttled(msg string) (ok bool, skipped int) {
	logThrottle.Lock()
	defer logThrottle.Unlock()
	t := time.Now()
	if t.Sub(logThrottle.last[msg]) < logEvery {
		logThrottle.suppressed[msg]++
		return false, 0
	}
	if len(logThrottle.last) > 1000 { // messages fixes : ne devrait jamais arriver
		clear(logThrottle.last)
	}
	logThrottle.last[msg] = t
	skipped = logThrottle.suppressed[msg]
	delete(logThrottle.suppressed, msg)
	return true, skipped
}

// throttledLog : avertissement limité à une ligne par minute pour un même message.
func throttledLog(msg string, args ...any) {
	if ok, skipped := throttled(msg); ok {
		slog.Warn(msg, append(args, "sautés", skipped)...)
	}
}

// errorLog : erreur interne, limitée à une ligne par minute pour un même contexte.
func errorLog(context string, err error) {
	if ok, skipped := throttled("erreur : " + context); ok {
		slog.Error(context, "err", err, "sautés", skipped)
	}
}
