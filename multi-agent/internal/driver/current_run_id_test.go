package driver

import (
	"sync"
	"testing"
	"time"
)

func TestSetCurrentRunID_RoundTrip(t *testing.T) {
	resetCurrentRunIDForTest()
	if got := CurrentRunID(); got != "" {
		t.Fatalf("zero value should be empty, got %q", got)
	}
	SetCurrentRunID("run-abc12345")
	if got := CurrentRunID(); got != "run-abc12345" {
		t.Fatalf("got %q want %q", got, "run-abc12345")
	}
}

func TestCurrentRunID_ZeroValueEmpty(t *testing.T) {
	resetCurrentRunIDForTest()
	if got := CurrentRunID(); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestSetCurrentRunID_ConcurrentSafe(t *testing.T) {
	resetCurrentRunIDForTest()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				select {
				case <-stop:
					return
				default:
				}
				SetCurrentRunID("run-abc12345")
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := CurrentRunID()
				if got != "" && got != "run-abc12345" {
					t.Errorf("garbage: %q", got)
					return
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
