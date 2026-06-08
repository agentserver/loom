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

func TestParseEndpointArgRejectsEmptyJSONEndpointFields(t *testing.T) {
	for _, arg := range []string{
		`{"network":"","address":"127.0.0.1:1234"}`,
		`{"network":"tcp","address":""}`,
	} {
		if _, err := ParseEndpointArg(arg); err == nil {
			t.Fatalf("ParseEndpointArg(%s) succeeded, want error", arg)
		}
	}
}

func TestParseEndpointArgRejectsUnsupportedNetwork(t *testing.T) {
	if _, err := ParseEndpointArg(`{"network":"udp","address":"127.0.0.1:1234"}`); err == nil {
		t.Fatal("ParseEndpointArg accepted unsupported network")
	}
}

func TestParseEndpointArgRejectsNonLoopbackTCP(t *testing.T) {
	for _, arg := range []string{
		`{"network":"tcp","address":"0.0.0.0:1234"}`,
		`{"network":"tcp","address":"192.168.1.1:1234"}`,
	} {
		if _, err := ParseEndpointArg(arg); err == nil {
			t.Fatalf("ParseEndpointArg(%s) succeeded, want error", arg)
		}
	}
}

func TestParseEndpointArgAcceptsLoopbackTCP(t *testing.T) {
	for _, arg := range []string{
		`{"network":"tcp","address":"127.0.0.1:1234"}`,
		`{"network":"tcp","address":"localhost:1234"}`,
		`{"network":"tcp","address":"[::1]:1234"}`,
	} {
		if _, err := ParseEndpointArg(arg); err != nil {
			t.Fatalf("ParseEndpointArg(%s): %v", arg, err)
		}
	}
}

func TestParseEndpointArgInvalidJSONDoesNotFallBackToPath(t *testing.T) {
	if _, err := ParseEndpointArg(`{"network":"tcp"`); err == nil {
		t.Fatal("ParseEndpointArg accepted invalid JSON as legacy path")
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
