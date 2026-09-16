package host

import (
	"net/http"
	"strconv"
	"strings"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func accessLog(next http.Handler, write func(string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if omitAccessLog(r.URL.Path) {
			return
		}
		write(r.Method + " " + r.URL.Path + " " + strconv.Itoa(sw.status))
	})
}

func omitAccessLog(path string) bool {
	return strings.HasPrefix(path, "/lobby/heartbeat")
}
