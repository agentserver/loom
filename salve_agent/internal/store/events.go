package store

type EventType string

const (
	EventChunk      EventType = "chunk"
	EventCapability EventType = "capability"
	EventDone       EventType = "done"
	EventError      EventType = "error"
)

type Event struct {
	Type EventType
	Data string
}
