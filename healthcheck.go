package main

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// healthcheck reports whether the exporter accepts connections on port on the
// loopback interface. It only opens a TCP connection, so it works whether or
// not TLS and authentication are enabled, and needs no credentials. It exists
// for the container HEALTHCHECK, because the distroless image contains no curl.
func healthcheck(port string, timeout time.Duration) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.Dial("tcp", (&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: n}).String())
	if err != nil {
		return err
	}
	return conn.Close()
}
