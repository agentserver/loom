package executor

import "github.com/yourorg/multi-agent/internal/observer"

type fakeObserver struct {
	events []observer.Event
}

func (f *fakeObserver) Emit(ev observer.Event) {
	f.events = append(f.events, ev)
}

func observerEventOfType(events []observer.Event, eventType string) (observer.Event, bool) {
	for _, ev := range events {
		if ev.Type == eventType {
			return ev, true
		}
	}
	return observer.Event{}, false
}

type nopSink struct{}

func (*nopSink) Write(string, string) {}
func (*nopSink) Close()               {}
