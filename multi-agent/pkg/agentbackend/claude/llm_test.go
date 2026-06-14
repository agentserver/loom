package claude

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerEchoesStdin(t *testing.T) {
	fakeBin := buildFakeClaude(t, `package main
import (
	"bufio"
	"fmt"
	"os"
)
func main() {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Print(line)
}
`)
	cfg := agentbackend.Config{Bin: fakeBin, ExtraArgs: nil}
	llm := newLLM(cfg, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ping" {
		t.Fatalf("out=%q want %q", out, "ping")
	}
}
