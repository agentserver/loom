package humanloop

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEndpointArgRoundTrip(t *testing.T) {
	in := Endpoint{Network: "tcp", Address: "127.0.0.1:49152"}
	arg := EndpointArg(in)
	got, err := ParseEndpointArg(arg)
	if err != nil {
		t.Fatalf("ParseEndpointArg: %v", err)
	}
	if got != in {
		t.Fatalf("endpoint = %+v, want %+v", got, in)
	}
}

func TestParseEndpointArgAcceptsLegacyUnixPath(t *testing.T) {
	got, err := ParseEndpointArg("/tmp/hl.sock")
	if err != nil {
		t.Fatalf("ParseEndpointArg legacy path: %v", err)
	}
	if got.Network != "unix" || got.Address != "/tmp/hl.sock" {
		t.Fatalf("legacy endpoint = %+v", got)
	}
}

func TestIPCRoundTrip(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()
	if ep.Network == "" || ep.Address == "" {
		t.Fatalf("empty endpoint: %+v", ep)
	}

	received := make(chan Payload, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			t.Errorf("Receive: %v", err)
			return
		}
		received <- p
	}()

	client, err := DialIPC(ep)
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
