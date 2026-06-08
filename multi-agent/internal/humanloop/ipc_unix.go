//go:build !windows

package humanloop

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func listenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, Endpoint{}, err
	}
	path := filepath.Join(baseDir, "hl.sock")
	_ = os.Remove(path) // best-effort: drop stale socket
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("humanloop listen %s: %w", path, err)
	}
	return &IPCServer{
		ln: ln,
		cleanup: func() {
			_ = os.Remove(path)
		},
	}, Endpoint{Network: "unix", Address: path}, nil
}
