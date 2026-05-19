package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type BashConfig struct {
	WorkDir string
}

type BashExecutor struct {
	cfg BashConfig
}

type BashRequest struct {
	Script     string            `json:"script"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type BashResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	WorkDir  string `json:"workdir"`
}

func NewBashExecutor(cfg BashConfig) *BashExecutor {
	return &BashExecutor{cfg: cfg}
}

func (e *BashExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req BashRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("bash prompt must be JSON: %w", err)
	}
	if req.Script == "" {
		return Result{}, fmt.Errorf("bash script is required")
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

	runCtx := ctx
	cancel := func() {}
	if req.TimeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/bash", "-lc", req.Script)
	cmd.Dir = workdir
	cmd.Env = cmd.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	result := BashResult{
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
			return Result{Summary: string(body)}, fmt.Errorf("bash timeout")
		}
		return Result{Summary: string(body)}, fmt.Errorf("bash exit code %d", exitCode)
	}
	return Result{Summary: string(body)}, nil
}
