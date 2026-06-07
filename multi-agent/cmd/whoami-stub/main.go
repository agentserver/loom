package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
)

type config struct {
	Tokens        map[string]whoami `json:"tokens"`
	RevokedTokens []string          `json:"revoked_tokens"`
}

type whoami struct {
	UserID        string `json:"user_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	SandboxID     string `json:"sandbox_id"`
	ShortID       string `json:"short_id"`
	Role          string `json:"role"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "listen address")
	configPath := flag.String("config", "", "JSON config path")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("--config is required")
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	revoked := map[string]bool{}
	for _, tok := range cfg.RevokedTokens {
		revoked[tok] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if revoked[token] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ident, ok := cfg.Tokens[token]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ident)
	})
	log.Fatal(http.ListenAndServe(*listen, mux))
}

func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, err
	}
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]whoami{}
	}
	return cfg, nil
}

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return token, token != ""
}
