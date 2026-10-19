package gazetteer

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
	"time"
)

// Watcher keeps track of which gazetteer build is active, so the API can report its version
// without a restart and can say "not built yet" instead of failing to start. Searches already follow
// the active build on every query (they read the gaz.current_* views); this only refreshes the label.
type Watcher struct {
	db    *sql.DB
	log   *slog.Logger
	state atomic.Pointer[watchState]
}

type watchState struct {
	label string
	ok    bool
}

// Watch polls the database every interval until ctx ends. The first check is synchronous.
func Watch(ctx context.Context, db *sql.DB, interval time.Duration, log *slog.Logger) *Watcher {
	w := &Watcher{db: db, log: log}
	w.state.Store(&watchState{})
	w.refresh(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.refresh(ctx)
			}
		}
	}()
	return w
}

func (w *Watcher) refresh(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, label, err := ActiveVersion(cctx, w.db)
	prev := w.state.Load()
	if err != nil {
		if prev.ok && w.log != nil {
			w.log.Warn("gazetteer no longer active", "err", err.Error())
		}
		// a failed check while a build was active keeps the last known label: a database blip must not
		// turn every geocode into a 503
		if !prev.ok || isNoVersion(err) {
			w.state.Store(&watchState{})
		}
		return
	}
	if !prev.ok || prev.label != label {
		if w.log != nil {
			w.log.Info("gazetteer active", "version", label)
		}
	}
	w.state.Store(&watchState{label: label, ok: true})
}

func isNoVersion(err error) bool {
	return err != nil && err.Error() == "no active gazetteer version: run the gazetteer builder with -activate"
}

// Label returns the active build's label, and false if no build is active yet.
func (w *Watcher) Label() (string, bool) {
	s := w.state.Load()
	return s.label, s.ok
}
