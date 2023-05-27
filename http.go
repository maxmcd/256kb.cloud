package main

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/exp/slog"
)

// Logs incoming requests, including response status.
func logMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := &responseObserver{ResponseWriter: w}
		start := time.Now()
		h.ServeHTTP(o, r)
		addr := r.RemoteAddr
		if i := strings.LastIndex(addr, ":"); i != -1 {
			addr = addr[:i]
		}
		slog.Info("request",
			"addr", addr,
			"host", r.Host,
			"method", r.Method,
			"url", r.URL,
			"proto", r.Proto,
			"status", o.status,
			"bytes", prettyByteSize(o.written),
			"referer", r.Referer(),
			"duration", time.Since(start),
		)
	})
}

type responseObserver struct {
	http.ResponseWriter
	http.Hijacker
	status      int
	written     int
	wroteHeader bool
}

func (o *responseObserver) Write(p []byte) (n int, err error) {
	if !o.wroteHeader {
		o.WriteHeader(http.StatusOK)
	}
	n, err = o.ResponseWriter.Write(p)
	o.written += n
	return
}

func (o *responseObserver) WriteHeader(code int) {
	o.ResponseWriter.WriteHeader(code)
	if o.wroteHeader {
		return
	}
	o.wroteHeader = true
	o.status = code
}

func (w *responseObserver) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func prettyByteSize(b int) string {
	bf := float64(b)
	for _, unit := range []string{"", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei", "Zi"} {
		if math.Abs(bf) < 1024.0 {
			return fmt.Sprintf("%3.1f%sB", bf, unit)
		}
		bf /= 1024.0
	}
	return fmt.Sprintf("%.1fYiB", bf)
}
