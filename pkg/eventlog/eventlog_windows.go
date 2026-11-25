package eventlog

import "golang.org/x/sys/windows/svc/eventlog"

// write reports msg under Source in the Application log.
// Writing doesn't need admin rights: the Application log accepts events from interactive users, which covers the
// per-user scan task.
func write(l level, id uint32, msg string) {
	source, err := eventlog.Open(Source)
	if err != nil {
		return
	}
	defer func() { _ = source.Close() }()

	switch l {
	case levelError:
		_ = source.Error(id, msg)
	case levelWarning:
		_ = source.Warning(id, msg)
	default:
		_ = source.Info(id, msg)
	}
}
