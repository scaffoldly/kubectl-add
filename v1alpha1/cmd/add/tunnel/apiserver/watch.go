// Package apiserver holds the libtunnel interceptors for an API-server tunnel.
//
// Kubernetes watches stream a chunked response that never ends, but Cloudflare's
// edge buffers each streamed chunk until it crosses a size threshold — so a
// small watch event pools at the edge instead of reaching the client, and a
// WatchList (a watch that also delivers the initial list via sendInitialEvents)
// never syncs. InterceptWatch defeats that by padding each event out past the
// threshold so it flushes immediately.
package apiserver

import (
	"bytes"
	"log/slog"
	"net/http"

	"github.com/cnuss/libtunnel"
)

// watchListPad is the size a WatchList event is padded up to, to push
// Cloudflare's edge past its streaming-buffer threshold and force a flush.
const watchListPad = 128 * 1024

// InterceptWatch targets a WatchList request — a streaming watch that also
// carries the initial list (sendInitialEvents), the k9s-style fingerprint — and
// pads its response with inter-event whitespace, forcing Cloudflare's edge past
// its streaming buffer so events flush to the client instead of pooling. A
// classic (change-feed) watch has no sendInitialEvents and is left to proxy
// normally.
func InterceptWatch(log *slog.Logger) libtunnel.Interceptor {
	return libtunnel.Interceptor{
		Match: func(r *http.Request) bool {
			q := r.URL.Query()
			return q.Get("watch") == "true" && q.Get("sendInitialEvents") == "true"
		},
		Handler: func(ictx libtunnel.InterceptCtx) libtunnel.InterceptCtx {
			log.Debug("watchlist interceptor fired", "path", ictx.Request().URL.Path)
			origin := ictx.Handler()
			return ictx.WithHandler(func(w http.ResponseWriter, r *http.Request) {
				origin(&padFlushWriter{ResponseWriter: w}, r)
			})
		},
	}
}

// padFlushWriter wraps the origin response for a WatchList. Every object-
// boundary write is padded out to watchListPad with whitespace — inert between
// JSON events — so each chunk crosses the edge threshold and flushes
// immediately: the initial batch to sync the informer, and every live event
// after it in real time. The cost is ~watchListPad of padding per event.
type padFlushWriter struct {
	http.ResponseWriter
}

func (p *padFlushWriter) Write(b []byte) (int, error) {
	n, err := p.ResponseWriter.Write(b)
	if err == nil && endsAtJSONBoundary(b[:n]) {
		if pad := watchListPad - n; pad > 0 {
			_, err = p.ResponseWriter.Write(bytes.Repeat([]byte{' '}, pad))
		}
	}
	p.flush()
	return n, err
}

func (p *padFlushWriter) Flush() { p.flush() }

func (p *padFlushWriter) flush() {
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// endsAtJSONBoundary reports whether b's last non-whitespace byte is '}', a
// proxy for "this chunk ended after a complete watch-event object" — so padding
// whitespace after it lands between objects, not inside one.
func endsAtJSONBoundary(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '}':
			return true
		default:
			return false
		}
	}
	return false
}
