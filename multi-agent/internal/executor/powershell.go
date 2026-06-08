package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type PowerShellConfig struct {
	WorkDir string
	Bin     string
}

type PowerShellExecutor struct {
	cfg PowerShellConfig
}

type PowerShellRequest struct {
	Script     string            `json:"script"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type PowerShellResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	WorkDir  string `json:"workdir"`
}

func NewPowerShellExecutor(cfg PowerShellConfig) *PowerShellExecutor {
	return &PowerShellExecutor{cfg: cfg}
}

func powerShellArgs(script string) []string {
	wrapped := fmt.Sprintf("& { %s }; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }", script)
	return []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", wrapped}
}

func (e *PowerShellExecutor) resolveBin() (string, error) {
	if e.cfg.Bin != "" {
		return e.cfg.Bin, nil
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe", nil
	}
	for _, name := range []string{"pwsh", "powershell"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("powershell binary not found")
}

func (e *PowerShellExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req PowerShellRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("powershell prompt must be JSON: %w", err)
	}
	if req.Script == "" {
		return Result{}, fmt.Errorf("powershell script is required")
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Result{}, err
	}
	bin, err := e.resolveBin()
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	cancel := func() {}
	if req.TimeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, powerShellArgs(req.Script)...)
	cmd.Dir = workdir
	cmd.Env = cmd.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	result := PowerShellResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		WorkDir:  workdir,
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return Result{}, marshalErr
	}
	sink.Write("chunk", string(body))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Summary: string(body)}, fmt.Errorf("powershell timeout")
		}
		if _, ok := err.(*exec.ExitError); !ok {
			return Result{Summary: string(body)}, fmt.Errorf("powershell start: %w", err)
		}
		return Result{Summary: string(body)}, fmt.Errorf("powershell exit code %d", exitCode)
	}
	return Result{Summary: string(body)}, nil
}
