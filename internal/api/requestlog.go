package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mcchukwu/address-normalization-geocoding-api/internal/metrics"
	"github.com/mcchukwu/address-normalization-geocoding-api/internal/store"
)

type logItem struct {
	req *store.RequestRecord
	raw *store.RawText
}

// RequestLogger writes request-log rows off the request path. If the queue is full the row is
// dropped and counted: bookkeeping must never slow down or fail an answer.
type RequestLogger struct {
	st      store.Store
	ch      chan logItem
	wg      sync.WaitGroup
	metrics *metrics.Metrics
	log     *slog.Logger
}

// NewRequestLogger starts the writer goroutine.
func NewRequestLogger(st store.Store, size int, m *metrics.Metrics, log *slog.Logger) *RequestLogger {
	l := &RequestLogger{st: st, ch: make(chan logItem, size), metrics: m, log: log}
	l.wg.Add(1)
	go l.run()
	return l
}

// Log queues one item without blocking.
func (l *RequestLogger) Log(it logItem) {
	select {
	case l.ch <- it:
	default:
		l.metrics.LogDropped()
	}
}

// Close drains the queue and stops the writer.
func (l *RequestLogger) Close() {
	close(l.ch)
	l.wg.Wait()
}

func (l *RequestLogger) run() {
	defer l.wg.Done()
	for it := range l.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if it.req != nil {
			if err := l.st.LogRequest(ctx, *it.req); err != nil {
				l.log.Warn("request log write failed", "err", err.Error())
			}
		}
		if it.raw != nil {
			if err := l.st.LogRawText(ctx, *it.raw); err != nil {
				l.log.Warn("raw text write failed", "err", err.Error())
			}
		}
		cancel()
	}
}
