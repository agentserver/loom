package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestPublishCardIncludesPlatform(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &driver.Config{
		Server:    driver.ServerConfig{URL: srv.URL, Name: "driver"},
		Discovery: driver.Discovery{DisplayName: "driver", Description: "d", Skills: []string{"chat"}},
	}

	require.NoError(t, publishCard(cfg))

	card, _ := got["card"].(map[string]interface{})
	require.NotNil(t, card, "missing card: %v", got)
	platform, _ := card["platform"].(map[string]interface{})
	require.Equal(t, map[string]interface{}{"os": runtime.GOOS, "arch": runtime.GOARCH}, platform)
}

func TestResolveDriverLocalPathUsesAuditLogDir(t *testing.T) {
	cfg := &driver.Config{}
	cfg.Credentials.ShortID = "drv-001"
	cfg.DriverDefaults.AuditLogDir = t.TempDir()

	auditPath, err := resolveDriverLocalPath(cfg, "audit.log")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.DriverDefaults.AuditLogDir, "audit.log"), auditPath)

	journalPath, err := resolveDriverLocalPath(cfg, "driver-tasks.jsonl")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.DriverDefaults.AuditLogDir, "driver-tasks.jsonl"), journalPath)
}

func TestServeDaemon_ParseFlagsConfigRequired(t *testing.T) {
	if _, err := parseServeDaemonFlags([]string{}); err == nil {
		t.Fatal("expected error for missing --config")
	}
}

func TestServeDaemon_ParseFlagsConfigParsed(t *testing.T) {
	opts, err := parseServeDaemonFlags([]string{"--config", "/tmp/x.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.ConfigPath != "/tmp/x.yaml" {
		t.Errorf("ConfigPath=%q", opts.ConfigPath)
	}
}

func TestServeDaemon_ParseFlagsListenOverride(t *testing.T) {
	opts, err := parseServeDaemonFlags([]string{
		"--config", "/tmp/x.yaml",
		"--listen", "0.0.0.0:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Listen != "0.0.0.0:8080" {
		t.Errorf("Listen=%q", opts.Listen)
	}
}

func TestDaemonWSURLConvertsHTTPToInsecureWS(t *testing.T) {
	got, insecure := daemonWSURL("http://observer.example/", "/api/daemon-link")
	require.Equal(t, "ws://observer.example/api/daemon-link", got)
	require.True(t, insecure)
}

func TestDaemonWSURLConvertsHTTPSToSecureWSS(t *testing.T) {
	got, insecure := daemonWSURL("https://observer.example/", "/api/daemon-link")
	require.Equal(t, "wss://observer.example/api/daemon-link", got)
	require.False(t, insecure)
}

func TestNewAgentBackendUsesDriverAgentKind(t *testing.T) {
	cfg := &driver.Config{}
	cfg.Agent.Kind = "claude"
	cfg.Agent.Bin = "custom-claude"
	cfg.Agent.WorkDir = t.TempDir()
	cfg.Agent.ExtraArgs = []string{"--debug"}

	backend, err := newAgentBackend(cfg)
	require.NoError(t, err)
	require.Equal(t, agentbackend.KindClaude, backend.Kind())
}

func TestNewAgentBackendReturnsUnknownKindError(t *testing.T) {
	cfg := &driver.Config{}
	cfg.Agent.Kind = "missing"

	_, err := newAgentBackend(cfg)
	require.Error(t, err)
}
