package plugins

// Run-status codes stored in the plugins.last_status column (the lean alternative
// to a per-invocation table, mirroring the webhooks delivery-status pattern).
const (
	statusNever int64 = 0 // never run
	statusOK    int64 = 1 // last run succeeded
	statusError int64 = 2 // last run failed
)

// maxErrorLen caps the stored last_error so a verbose plugin error can't bloat the
// row (matches the webhooks dispatcher).
const maxErrorLen = 500

// plugin is one loaded, running plugin: its DB identity, the loaded instance, the
// event-type -> hook routing table, and its own bounded job queue drained by a
// single worker goroutine (so the non-reentrant VM is only ever touched from one
// goroutine and a slow plugin can't starve others).
type plugin struct {
	id       int64
	name     string
	manifest Manifest
	inst     Instance
	triggers map[string]Hook // event type -> the hook to invoke
	jobs     chan job
	done     chan struct{} // closed when the worker goroutine exits

	// lastSuccessAt is cached so recordStatus can preserve it on a failed run
	// without a read. Touched only by the (single) worker goroutine.
	lastSuccessAt int64
}

// job is one hook invocation queued for a plugin's worker.
type job struct {
	hook Hook
	ev   HookEvent
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
