//go:build windows

package humanloop

import (
	"fmt"
	"net"
)

func listenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("humanloop listen tcp: %w", err)
	}
	return &IPCServer{ln: ln}, Endpoint{Network: "tcp", Address: ln.Addr().String()}, nil
}
