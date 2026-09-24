package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestHashPassword(t *testing.T) {
	for _, input := range []string{"example-password\n", "example-password\r\n", "example-password"} {
		var out bytes.Buffer
		require.NoError(t, hashPassword(strings.NewReader(input), &out))

		hash := strings.TrimSpace(out.String())
		assert.True(t, strings.HasPrefix(hash, "$2"), "bcrypt hash expected, got %q", hash)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("example-password")))
		assert.Error(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("other")))
	}
}

func TestHashPasswordRejectsEmptyInput(t *testing.T) {
	for _, input := range []string{"", "\n"} {
		var out bytes.Buffer
		assert.Error(t, hashPassword(strings.NewReader(input), &out))
		assert.Empty(t, out.String())
	}
}

func TestHealthcheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	assert.NoError(t, healthcheck(port, time.Second), "listening port is healthy")

	require.NoError(t, ln.Close())
	assert.Error(t, healthcheck(port, time.Second), "closed port is unhealthy")
}

func TestHealthcheckRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"", "0", "70000", "abc", "8080; rm -rf /", "example.com:80"} {
		assert.Error(t, healthcheck(port, time.Second), port)
	}
}

func TestRunCommandHealthcheckUsesListenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	t.Setenv("LISTEN_PORT", port)
	assert.Equal(t, 0, runCommand([]string{"healthcheck"}))

	require.NoError(t, ln.Close())
	assert.Equal(t, 1, runCommand([]string{"healthcheck"}))
}

func TestRunCommandUnknown(t *testing.T) {
	assert.Equal(t, 2, runCommand([]string{"bogus"}))
}
