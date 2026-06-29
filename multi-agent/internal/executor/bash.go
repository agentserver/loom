package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

type BashConfig struct {
	WorkDir string
	Bin     string
	Args    []string
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
		return Result{}, observerstore.Categorize(fmt.Errorf("bash prompt must be JSON: %w", err), observerstore.FailContractViolation)
	}
	if req.Script == "" {
		return Result{}, observerstore.Categorize(fmt.Errorf("bash script is required"), observerstore.FailContractViolation)
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return Result{}, observerstore.Categorize(err, observerstore.FailMissingFile)
		}
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Result{}, observerstore.Categorize(err, observerstore.FailMissingFile)
	}

	runCtx := ctx
	cancel := func() {}
	if req.TimeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	}
	defer cancel()

	bin := e.cfg.Bin
	if bin == "" {
		bin = "/bin/bash"
	}
	args := append([]string{}, e.cfg.Args...)
	if len(args) == 0 {
		args = []string{"-lc"}
	}
	args = append(args, req.Script)
	cmd := exec.CommandContext(runCtx, bin, args...)
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
		return Result{}, observerstore.Categorize(marshalErr, observerstore.FailUnknown)
	}
	sink.Write("chunk", string(body))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Summary: string(body)}, observerstore.Categorize(fmt.Errorf("bash timeout"), observerstore.FailTimeout)
		}
		// Non-zero exit from the user-supplied script is a content failure,
		// not a system one — leave it untagged (FailUnknown) so analytics
		// don't bucket "user's grep returned 1" with infrastructure faults.
		return Result{Summary: string(body)}, fmt.Errorf("bash exit code %d", exitCode)
	}
	return Result{Summary: string(body)}, nil
}
