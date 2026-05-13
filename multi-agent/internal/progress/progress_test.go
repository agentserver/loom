package progress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunWithHeartbeatEmitsProgressUntilFunctionReturns(t *testing.T) {
	var messages []string
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    10 * time.Millisecond,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit: func(_ context.Context, elapsed time.Duration) {
			messages = append(messages, elapsed.String())
		},
	}, func(ctx context.Context) error {
		time.Sleep(25 * time.Millisecond)
		return nil
	})

	require.NoError(t, err)
	require.NotEmpty(t, messages)
}

func TestRunWithHeartbeatHardTimeout(t *testing.T) {
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    10 * time.Millisecond,
		HardTimeout: 20 * time.Millisecond,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "hard timeout")
}

func TestRunWithHeartbeatReturnsHardTimeoutWhenFunctionReturnsContextErrorAfterDeadline(t *testing.T) {
	parent := context.Background()

	for i := 0; i < 100; i++ {
		err := RunWithHeartbeat(parent, Config{
			Interval:    time.Millisecond,
			HardTimeout: 5 * time.Millisecond,
			Message:     "still running",
			Emit: func(context.Context, time.Duration) {
				time.Sleep(10 * time.Millisecond)
			},
		}, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "hard timeout")
	}
}

func TestRunWithHeartbeatReturnsHardTimeoutWhenFunctionReturnsNilAfterDeadline(t *testing.T) {
	parent := context.Background()

	for i := 0; i < 100; i++ {
		err := RunWithHeartbeat(parent, Config{
			Interval:    time.Millisecond,
			HardTimeout: 5 * time.Millisecond,
			Message:     "still running",
			Emit: func(context.Context, time.Duration) {
				time.Sleep(10 * time.Millisecond)
			},
		}, func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "hard timeout")
	}
}

func TestRunWithHeartbeatIdleTimeout(t *testing.T) {
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    time.Hour,
		IdleTimeout: 25 * time.Millisecond,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "idle timeout")
}

func TestRunWithHeartbeatReturnsFunctionError(t *testing.T) {
	want := errors.New("boom")
	err := RunWithHeartbeat(context.Background(), Config{
		Interval:    time.Hour,
		HardTimeout: time.Second,
		Message:     "still running",
		Emit:        func(context.Context, time.Duration) {},
	}, func(ctx context.Context) error {
		return want
	})

	require.ErrorIs(t, err, want)
}
