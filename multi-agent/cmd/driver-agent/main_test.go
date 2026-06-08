package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/driver"
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
