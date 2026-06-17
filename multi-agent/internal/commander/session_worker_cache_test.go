package commander

import (
	"context"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type blockingCloseWorker struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
}

func (w *blockingCloseWorker) Run(context.Context, string, executor.Sink) (executor.Result, error) {
	return executor.Result{}, nil
}

func (w *blockingCloseWorker) Close() error {
	close(w.closeStarted)
	<-w.releaseClose
	return nil
}

func TestSessionWorkerCacheRemoveDoesNotHoldLockDuringWorkerClose(t *testing.T) {
	cache := newSessionWorkerCache(1, time.Hour)
	defer cache.closeAll()
	worker := &blockingCloseWorker{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	entry, err := cache.acquire(context.Background(), sessionWorkerKey{sessionID: "s1"}, func(context.Context) (agentbackend.SessionWorker, error) {
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cache.release(entry)

	removeDone := make(chan struct{})
	go func() {
		cache.remove(entry)
		close(removeDone)
	}()
	select {
	case <-worker.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("worker close did not start")
	}
	releasedClose := false
	defer func() {
		if !releasedClose {
			close(worker.releaseClose)
		}
	}()

	activeDone := make(chan struct{})
	go func() {
		_ = cache.activeKeys()
		close(activeDone)
	}()
	select {
	case <-activeDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("activeKeys blocked while worker.Close was running")
	}

	releasedClose = true
	close(worker.releaseClose)
	select {
	case <-removeDone:
	case <-time.After(time.Second):
		t.Fatal("remove did not finish after worker.Close released")
	}
}
