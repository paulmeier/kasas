package plugins

import "encoding/json"

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
	caps     capSet // effective grants (manifest ∩ DB), for gating the page endpoints
	inst     Instance
	triggers map[string]Hook // event type -> the hook to invoke
	jobs     chan job
	done     chan struct{} // closed when the worker goroutine exits

	// lastSuccessAt is cached so recordStatus can preserve it on a failed run
	// without a read. Touched only by the (single) worker goroutine.
	lastSuccessAt int64
}

// job is one unit of work queued for a plugin's worker: an event-hook invocation
// (reply == nil, produce == nil), a synchronous page render/action (req + reply
// set), or a producer fetch (payload + produce set, ADR 0005). The three share the
// queue so the non-reentrant VM is still only ever touched by the one worker
// goroutine, serialized with event hooks.
type job struct {
	hook Hook
	ev   HookEvent

	// Render request: the page hook's input and the channel the worker answers on.
	// reply is buffered (cap 1) so the worker never blocks if the requester has
	// already given up waiting.
	req   *PageRequest
	reply chan renderReply

	// Produce request: the OnFetch JSON payload ({since,cursor}) and the channel the
	// worker answers the ImportBatch on. produce is buffered (cap 1), same as reply.
	payload json.RawMessage
	produce chan produceReply
}

// renderReply is the worker's answer to a page job: the VALIDATED, normalized
// page document, or the error (from the hook or from validation).
type renderReply struct {
	doc json.RawMessage
	err error
}

// produceReply is the worker's answer to a produce job: the raw (still untrusted)
// ImportBatch JSON the OnFetch hook returned, or the error.
type produceReply struct {
	batch json.RawMessage
	err   error
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
