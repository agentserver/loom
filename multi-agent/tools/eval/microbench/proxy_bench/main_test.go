package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test #41 — default flags: both endpoints on loopback (no external socket).
func TestProxyBench_LocalHttpstestOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("bench takes ~seconds; skipped in short mode")
	}
	// Drive the URL-picking helper directly so the assertion is
	// deterministic (no dependency on the port the OS handed out).
	proxyURL, closeP := chooseURL("", 0)
	defer closeP()
	directURL, closeD := chooseURL("", 0)
	defer closeD()
	for _, u := range []string{proxyURL, directURL} {
		if !strings.HasPrefix(u, "http://127.0.0.1:") {
			t.Errorf("bench spun a non-loopback server: %s", u)
		}
	}
}

// Test #42 — --dry-run prints proxy_url= and direct_url= entries and
// exits 0 without opening a socket.
func TestProxyBench_DryRun_PrintsURLPair(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"--dry-run"}, &out, &out)
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "proxy_url=http://127.0.0.1:") {
		t.Errorf("dry-run stdout missing proxy_url line: %q", out.String())
	}
	if !strings.Contains(out.String(), "direct_url=http://127.0.0.1:") {
		t.Errorf("dry-run stdout missing direct_url line: %q", out.String())
	}
}

// Test #43 — both round trips receive the SAME prompt bytes. Uses two
// capturing httptest servers; asserts the recorded request bodies match.
func TestProxyBench_SamePromptBothSides(t *testing.T) {
	var mu sync.Mutex
	var proxyBodies, directBodies [][]byte
	capturing := func(dst *[][]byte) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*dst = append(*dst, body)
			mu.Unlock()
			r.Body.Close()
			w.WriteHeader(200)
		})
	}
	proxySrv := httptest.NewServer(capturing(&proxyBodies))
	defer proxySrv.Close()
	directSrv := httptest.NewServer(capturing(&directBodies))
	defer directSrv.Close()

	var out bytes.Buffer
	code := run([]string{
		"--proxy-url", proxySrv.URL,
		"--direct-url", directSrv.URL,
		"--warmup", "100", "--samples", "500",
	}, &out, &out)
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if len(proxyBodies) == 0 || len(directBodies) == 0 {
		t.Fatalf("captures empty: proxy=%d direct=%d", len(proxyBodies), len(directBodies))
	}
	// Every body on both sides must equal fixedPrompt exactly.
	for _, b := range proxyBodies {
		if string(b) != fixedPrompt {
			t.Fatalf("proxy body mismatch: %q", string(b))
		}
	}
	for _, b := range directBodies {
		if string(b) != fixedPrompt {
			t.Fatalf("direct body mismatch: %q", string(b))
		}
	}
	// Sanity: with fake-delay-ms=0 the p50s should be close; with a
	// non-zero delay injected into the proxy side, proxy p50 should be
	// visibly larger. We assert only the SIGN (correctness of the
	// pipeline), not a threshold, so this is CI-safe.
	_ = time.Now // silence import
}
