package agentbackend

const (
	StatusQueued           = "queued"
	StatusStarting         = "starting"
	StatusAnswering        = "answering"
	StatusAwaitingApproval = "awaiting_approval"
	StatusDone             = "done"
	StatusError            = "error"
)

type StatusSink interface {
	WriteStatus(statusCode, text string)
}

func WriteStatus(s Sink, statusCode, text string) {
	if ss, ok := s.(StatusSink); ok {
		ss.WriteStatus(statusCode, text)
		return
	}
	s.Write("status", text)
}
