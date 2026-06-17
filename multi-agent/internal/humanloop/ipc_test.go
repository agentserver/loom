package humanloop

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestEndpointArgRoundTrip(t *testing.T) {
	in := Endpoint{Network: "tcp", Address: "127.0.0.1:49152", Secret: "test-secret"}
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
		`{"network":"tcp","address":"127.0.0.1:1234","secret":""}`,
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
		`{"network":"tcp","address":"127.0.0.1:1234","secret":"test-secret"}`,
		`{"network":"tcp","address":"localhost:1234","secret":"test-secret"}`,
		`{"network":"tcp","address":"[::1]:1234","secret":"test-secret"}`,
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
	if ep.Secret == "" {
		t.Fatalf("endpoint missing secret: %+v", ep)
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

func TestIPCClientSendWaitsForServerAck(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()

	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()

	sendErr := make(chan error, 1)
	want := Payload{Kind: "ask_user", Question: "ack gated"}
	go func() {
		sendErr <- client.Send(want)
	}()

	pending, err := srv.ReceivePending()
	if err != nil {
		t.Fatalf("ReceivePending: %v", err)
	}
	if !payloadJSONEqual(pending.Payload, want) {
		t.Fatalf("pending payload = %+v, want %+v", pending.Payload, want)
	}

	select {
	case err := <-sendErr:
		t.Fatalf("Send returned before Ack with error %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := pending.Ack(); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("Send returned error after Ack: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after Ack")
	}
}

func TestIPCReceiveAndAckWaitsForCallbackBeforeAck(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()

	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()

	sendErr := make(chan error, 1)
	want := Payload{Kind: "ask_user", Question: "callback gated"}
	go func() {
		sendErr <- client.Send(want)
	}()

	callbackEntered := make(chan Payload, 1)
	releaseCallback := make(chan struct{})
	receiveErr := make(chan error, 1)
	go func() {
		receiveErr <- srv.ReceiveAndAck(func(p Payload) error {
			callbackEntered <- p
			<-releaseCallback
			return nil
		})
	}()

	got := receiveWithin(t, callbackEntered, "receive callback")
	if !payloadJSONEqual(got, want) {
		t.Fatalf("callback payload = %+v, want %+v", got, want)
	}
	select {
	case err := <-sendErr:
		t.Fatalf("Send returned before callback completed with error %v", err)
	default:
	}

	close(releaseCallback)
	select {
	case err := <-receiveErr:
		if err != nil {
			t.Fatalf("ReceiveAndAck returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReceiveAndAck did not return")
	}
	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("Send returned error after callback ack: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after callback ack")
	}
}

func TestIPCRejectsInvalidSecretBeforeReceivingValidPayload(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()
	if ep.Secret == "" {
		t.Fatal("ListenIPC returned endpoint without secret")
	}

	received := make(chan Payload, 1)
	errs := make(chan error, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			errs <- err
			return
		}
		received <- p
	}()

	wrong := ep
	wrong.Secret = "wrong-secret"
	if err := sendRawIPC(wrong, Payload{Kind: "ask_user", Question: "wrong"}); err != nil {
		t.Fatalf("send wrong secret: %v", err)
	}

	select {
	case got := <-received:
		t.Fatalf("server accepted payload with wrong secret: %+v", got)
	case err := <-errs:
		t.Fatalf("server returned after wrong secret instead of waiting for valid payload: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	want := Payload{Kind: "ask_user", Question: "valid"}
	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()
	if err := client.Send(want); err != nil {
		t.Fatalf("Send valid payload: %v", err)
	}

	select {
	case got := <-received:
		if !payloadJSONEqual(got, want) {
			t.Fatalf("payload = %+v, want %+v", got, want)
		}
	case err := <-errs:
		t.Fatalf("Receive returned error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for valid IPC payload")
	}
}

func TestIPCReadTimeoutPreventsSilentConnectionFromBlockingValidPayload(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()

	blocker, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		t.Fatalf("dial blocker: %v", err)
	}
	defer blocker.Close()

	received := make(chan Payload, 1)
	errs := make(chan error, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			errs <- err
			return
		}
		received <- p
	}()

	time.Sleep(20 * time.Millisecond)

	want := Payload{Kind: "ask_user", Question: "valid after blocker"}
	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()
	if err := client.Send(want); err != nil {
		t.Fatalf("Send valid payload: %v", err)
	}

	select {
	case got := <-received:
		if !payloadJSONEqual(got, want) {
			t.Fatalf("payload = %+v, want %+v", got, want)
		}
	case err := <-errs:
		t.Fatalf("Receive returned error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("silent unauthenticated connection blocked valid IPC payload")
	}
}

func TestIPCMalformedFrameDoesNotStopReceivingValidPayload(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()

	received := make(chan Payload, 1)
	errs := make(chan error, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			errs <- err
			return
		}
		received <- p
	}()

	if err := sendRawLine(ep, "not-json\n"); err != nil {
		t.Fatalf("send malformed frame: %v", err)
	}

	select {
	case got := <-received:
		t.Fatalf("server accepted malformed frame as payload: %+v", got)
	case err := <-errs:
		t.Fatalf("Receive returned after malformed frame instead of waiting for valid payload: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	want := Payload{Kind: "ask_user", Question: "valid after malformed frame"}
	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()
	if err := client.Send(want); err != nil {
		t.Fatalf("Send valid payload: %v", err)
	}

	select {
	case got := <-received:
		if !payloadJSONEqual(got, want) {
			t.Fatalf("payload = %+v, want %+v", got, want)
		}
	case err := <-errs:
		t.Fatalf("Receive returned error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("malformed unauthenticated frame stopped valid IPC payload")
	}
}

func sendRawIPC(ep Endpoint, p Payload) error {
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		return err
	}
	defer c.Close()
	b, err := json.Marshal(ipcMessage{Secret: ep.Secret, Payload: p})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.Write(b)
	return err
}

func sendRawLine(ep Endpoint, line string) error {
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(line))
	return err
}

func receiveWithin[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", label)
		return zero
	}
}

func payloadJSONEqual(a, b Payload) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
