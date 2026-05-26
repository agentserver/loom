package humanloop

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServerQuotaRefusesAfterMax(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hl.sock")
	srv, err := ListenIPC(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Drain payloads in background so srv.Receive doesn't block forever.
	go func() {
		for {
			if _, err := srv.Receive(); err != nil {
				return
			}
		}
	}()

	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
	}
	for i := 0; i < 3; i++ {
		lines = append(lines, `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"q?"}}}`)
	}

	resps := drive(t, sock, 2 /* MAX */, lines...)
	if len(resps) < 4 {
		t.Fatalf("expected ≥4 responses, got %d", len(resps))
	}
	// First two ask_user calls: "submitted"
	if !strings.Contains(resps[1], "submitted") || !strings.Contains(resps[2], "submitted") {
		t.Errorf("expected first two submitted, got %v", resps[1:3])
	}
	// Third one: refused
	if !strings.Contains(resps[3], "refused") {
		t.Errorf("expected third refused, got %s", resps[3])
	}
}
