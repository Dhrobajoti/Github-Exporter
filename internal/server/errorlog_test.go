package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsLoopbackProbe(t *testing.T) {
	tests := map[string]bool{
		"http: TLS handshake error from 127.0.0.1:39058: EOF":                                             true,
		"http: TLS handshake error from 127.0.0.1:39058: remote error: tls: bad certificate":              false,
		"http: TLS handshake error from 10.0.0.7:39058: EOF":                                              false,
		"http: TLS handshake error from 127.0.0.1:1: tls: client offered only unsupported versions":       false,
		"http: TLS handshake error from 192.168.1.5:4433: client sent an HTTP request to an HTTPS server": false,
		"http: Accept error: too many open files; retrying in 5ms":                                        false,
	}
	for message, want := range tests {
		assert.Equal(t, want, isLoopbackProbe(message), message)
	}
}

func TestNewErrorLogForwardsOnlyRealErrors(t *testing.T) {
	var got []string
	logger := NewErrorLog(func(m string) { got = append(got, m) })

	logger.Printf("http: TLS handshake error from 127.0.0.1:50000: EOF")
	logger.Printf("http: TLS handshake error from 10.1.2.3:50001: remote error: tls: bad certificate")
	logger.Printf("http: Accept error: boom")

	assert.Equal(t, []string{
		"http: TLS handshake error from 10.1.2.3:50001: remote error: tls: bad certificate",
		"http: Accept error: boom",
	}, got)
}

// TestHealthProbeAgainstRealTLSServer reproduces the real situation: a plain TCP
// connection to a TLS port that is closed without a handshake.
func TestHealthProbeAgainstRealTLSServer(t *testing.T) {
	var mu sync.Mutex
	var logged []string

	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config.ErrorLog = NewErrorLog(func(m string) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, m)
	})
	srv.StartTLS()
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	// The container health check: connect, then close.
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	// A client that sends plain HTTP to the TLS port is a real misconfiguration
	// and must still be reported.
	plain, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	_, err = plain.Write([]byte("GET /metrics HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)
	_ = plain.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	_, _ = plain.Read(buf)
	require.NoError(t, plain.Close())

	// A proper TLS client works normally.
	tlsConn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"})
	require.NoError(t, err)
	require.NoError(t, tlsConn.Close())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(logged) >= 1
	}, 3*time.Second, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let any late probe log line arrive

	mu.Lock()
	defer mu.Unlock()
	for _, m := range logged {
		assert.False(t, isLoopbackProbe(m), "health probe noise leaked into the log: %s", m)
	}
	assert.NotEmpty(t, logged, "the plain-HTTP-to-TLS-port error must still be logged")
}
