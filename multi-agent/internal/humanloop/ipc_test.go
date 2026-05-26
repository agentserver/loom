package humanloop

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestIPCRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hl.sock")

	srv, err := ListenIPC(sock)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()

	received := make(chan Payload, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			t.Errorf("Receive: %v", err)
			return
		}
		received <- p
	}()

	client, err := DialIPC(sock)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()

	in := Payload{Kind: "ask_user", Question: "are we good?", Options: []string{"yes", "no"}}
	if err := client.Send(in); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-received:
		gj, _ := json.Marshal(got)
		ij, _ := json.Marshal(in)
		if string(gj) != string(ij) {
			t.Errorf("payload mismatch:\nwant %s\ngot  %s", ij, gj)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for IPC payload")
	}
}
