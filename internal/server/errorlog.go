package server

import (
	stdlog "log"
	"strings"
)

// loopbackProbeMarker identifies the log line net/http writes when a client on
// the loopback interface connects and closes without starting a TLS handshake.
const loopbackProbeMarker = "TLS handshake error from 127.0.0.1:"

// NewErrorLog returns the logger to assign to http.Server.ErrorLog. It forwards
// the server's internal error messages to out, except for the handshake errors
// produced by the container health check, which opens a plain TCP connection to
// the TLS port every few seconds and would otherwise flood the log.
//
// Handshake errors from any other address (bad certificates, plain HTTP sent to
// the HTTPS port, ...) are still forwarded.
func NewErrorLog(out func(message string)) *stdlog.Logger {
	return stdlog.New(errorLogWriter{out: out}, "", 0)
}

type errorLogWriter struct {
	out func(message string)
}

func (w errorLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	if isLoopbackProbe(message) {
		return len(p), nil
	}
	w.out(message)
	return len(p), nil
}

func isLoopbackProbe(message string) bool {
	return strings.Contains(message, loopbackProbeMarker) && strings.HasSuffix(message, ": EOF")
}
