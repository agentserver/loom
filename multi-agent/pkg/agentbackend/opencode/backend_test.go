package opencode

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestOpencodeBackend_AutoRegisters pins the §issue-15 "new backend = one
// new package + one import" promise: importing this package registers the
// opencode builder so agentbackend.RegisteredKinds() includes it.
func TestOpencodeBackend_AutoRegisters(t *testing.T) {
	var found bool
	for _, k := range agentbackend.RegisteredKinds() {
		if k == string(agentbackend.KindOpencode) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("opencode not in RegisteredKinds(); got %v", agentbackend.RegisteredKinds())
	}
}

// TestOpencodeNew_DefaultsBin pins factory default: empty cfg.Bin becomes
// "opencode" (the npm-installed binary name).
func TestOpencodeNew_DefaultsBin(t *testing.T) {
	b := New(agentbackend.Config{WorkDir: t.TempDir()}, nil)
	if b.cfg.Bin != "opencode" {
		t.Fatalf("Bin=%q want opencode", b.cfg.Bin)
	}
	if b.Kind() != agentbackend.KindOpencode {
		t.Fatalf("Kind()=%v want opencode", b.Kind())
	}
}

// TestOpencodeNew_KeepsExplicitBin pins that an operator-set bin is
// preserved (not overwritten by the default).
func TestOpencodeNew_KeepsExplicitBin(t *testing.T) {
	b := New(agentbackend.Config{Bin: "/usr/local/bin/opencode-custom", WorkDir: t.TempDir()}, nil)
	if b.cfg.Bin != "/usr/local/bin/opencode-custom" {
		t.Fatalf("Bin=%q want /usr/local/bin/opencode-custom", b.cfg.Bin)
	}
}

// TestOpencodeBackend_RegistryDispatchesNew exercises the agentbackend
// registry path: agentbackend.New(Config{Kind: KindOpencode, ...}) routes
// through our init() builder and returns *Backend.
func TestOpencodeBackend_RegistryDispatchesNew(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindOpencode,
		Bin:     "opencode",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindOpencode {
		t.Fatalf("Kind()=%v want opencode", b.Kind())
	}
	_ = context.Background() // silence import if not used below
}
