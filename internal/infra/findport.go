package infra

import (
	"fmt"
	"net"
)

// FindFreePort finds a free TCP port in [start, end] by probing.
// Returns 0 and an error if no port is available in the range.
func FindFreePort(host string, start, end int) (int, error) {
	for port := start; port <= end; port++ {
		addr := fmt.Sprintf("%s:%d", host, port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %d-%d", start, end)
}
