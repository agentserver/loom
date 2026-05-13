package progress

import (
	"context"
	"fmt"
	"time"
)

type Config struct {
	Interval    time.Duration
	IdleTimeout time.Duration
	HardTimeout time.Duration
	Message     string
	Emit        func(context.Context, time.Duration)
}

func RunWithHeartbeat(parent context.Context, cfg Config, fn func(context.Context) error) error {
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	ctx := parent
	cancel := func() {}
	if cfg.HardTimeout > 0 {
		ctx, cancel = context.WithTimeout(parent, cfg.HardTimeout)
	}
	defer cancel()

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- fn(ctx)
	}()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	var idle <-chan time.Time
	var idleTimer *time.Timer
	if cfg.IdleTimeout > 0 {
		idleTimer = time.NewTimer(cfg.IdleTimeout)
		defer idleTimer.Stop()
		idle = idleTimer.C
	}

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if cfg.Emit != nil {
				cfg.Emit(ctx, time.Since(start))
			}
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(cfg.IdleTimeout)
			}
		case <-idle:
			cancel()
			return fmt.Errorf("idle timeout after %s", cfg.IdleTimeout)
		case <-ctx.Done():
			if cfg.HardTimeout > 0 && parent.Err() == nil {
				return fmt.Errorf("hard timeout after %s", cfg.HardTimeout)
			}
			return ctx.Err()
		}
	}
}
