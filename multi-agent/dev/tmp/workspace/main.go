// Command workspace is a small CLI for manual testing against the fixed
// /tmp/multi-agent-driver-first-e2e agentserver workspace. Use it to rebuild
// binaries from the current worktree, migrate persistent configs, and start
// or stop the slave containers without invoking the full online e2e flow.
//
// Usage:
//
//	go run ./dev/tmp/workspace <verb>
//
// Verbs:
//
//	status   show binary mtime/size and slave container state
//	build    rebuild driver-agent and slave-agent from current worktree
//	migrate  rewrite persistent slave configs (build_mcp -> register_mcp)
//	up       start slave containers (no rebuild)
//	down     stop slave containers
//	restart  down then up
//	prepare  build + migrate + restart (equivalent to the e2e's startup phase)
//
// Driver is intentionally not managed here: the e2e tool runs it as a
// one-shot stdio container, and manual driver invocations are typically done
// from a fresh `docker run` against /e2e/bin/driver-agent.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/yourorg/multi-agent/dev/tmp/e2eworkspace"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: workspace <status|build|migrate|up|down|restart|prepare>")
	os.Exit(2)
}

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	verb := os.Args[1]
	switch verb {
	case "status":
		e2eworkspace.Status(os.Stdout)
	case "build":
		mustBuild()
	case "migrate":
		mustMigrate()
	case "up":
		mustUp()
	case "down":
		mustDown()
	case "restart":
		mustDown()
		mustUp()
	case "prepare":
		mustBuild()
		mustMigrate()
		mustDown()
		mustUp()
	default:
		usage()
	}
}

func mustBuild() {
	moduleRoot, err := e2eworkspace.FindModuleRoot()
	if err != nil {
		die(err.Error())
	}
	fmt.Println("BUILT_FROM=" + moduleRoot)
	if err := e2eworkspace.BuildBinaries(context.Background(), moduleRoot, os.Stderr); err != nil {
		die(err.Error())
	}
}

func mustMigrate() {
	if err := e2eworkspace.MigrateRuntimeConfigs(os.Stdout); err != nil {
		die(err.Error())
	}
}

func mustUp() {
	for _, s := range e2eworkspace.SlaveContainers() {
		if err := e2eworkspace.StartSlaveContainer(s.Name, s.Workdir, os.Stderr); err != nil {
			die(err.Error())
		}
	}
}

func mustDown() {
	for _, s := range e2eworkspace.SlaveContainers() {
		_ = e2eworkspace.StopSlaveContainer(s.Name, os.Stderr)
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "workspace FAIL:", msg)
	os.Exit(1)
}
