package scriptstest

import (
	"strings"
	"testing"
)

func TestObserverScriptDryRunStartBuildsConfiguresAndStartsServer(t *testing.T) {
	out := runNamedScript(t, "scripts/observer.sh", "--dry-run", "start")

	for _, want := range []string{
		"go build -o bin/observer-server ./cmd/observer-server",
		"cp cmd/observer-server/config.example.yaml observer.yaml",
		"bin/observer-server --config observer.yaml",
		".run/observer/observer-server.pid",
		".run/observer/observer-server.log",
		"http://127.0.0.1:8090/",
		"http://127.0.0.1:8090/drivers",
		"http://127.0.0.1:8090/masters",
		"http://127.0.0.1:8090/slaves",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run observer start missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestObserverScriptDryRunStopUsesPidFileAndScopedFallback(t *testing.T) {
	out := runNamedScript(t, "scripts/observer.sh", "--dry-run", "stop")

	for _, want := range []string{
		".run/observer/observer-server.pid",
		"pkill -f",
		"bin/observer-server --config observer.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run observer stop missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestObserverScriptDryRunStatusMentionsLogPidAndViews(t *testing.T) {
	out := runNamedScript(t, "scripts/observer.sh", "--dry-run", "status")

	for _, want := range []string{
		"observer-server",
		".run/observer/observer-server.pid",
		".run/observer/observer-server.log",
		"http://127.0.0.1:8090/",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run observer status missing %q\noutput:\n%s", want, out)
		}
	}
}
