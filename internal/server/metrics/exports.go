package metrics

import (
	"runtime"
	"time"
)

// Named, package-level metric handles. Wired by callers via .With(...).Inc()
// or .Inc() for bare counters. The HELP strings here are the authoritative
// documentation of label cardinality.
var (
	WSConnections = NewCounterVec(
		"leyline_ws_connections_total",
		"Successful WebSocket sync upgrades (auth_ok written).",
		"vault",
	)
	WSAuthFailures = NewCounterVec(
		"leyline_ws_auth_failures_total",
		"WebSocket auth rejections. reason ∈ {no_auth_msg,invalid_key,invalid_role,rate_limited,plugin_outdated,session_limit}.",
		"vault", "reason",
	)
	SyncPushes = NewCounterVec(
		"leyline_sync_pushes_total",
		"Push outcomes. outcome ∈ {ok,merged,conflict,error}.",
		"vault", "outcome",
	)
	SyncPulls = NewCounterVec(
		"leyline_sync_pulls_total",
		"Pull requests handled.",
		"vault",
	)
	GitOps = NewCounterVec(
		"leyline_git_ops_total",
		"git-store operations. op ∈ {commit,commit_all,commit_deletion,revert,restore,tag}; result ∈ {ok,error}.",
		"vault", "op", "result",
	)
	GitGCRuns = NewCounterVec(
		"leyline_git_gc_runs_total",
		"git gc invocations from the daily scheduler. result ∈ {ok,error}.",
		"vault", "result",
	)
	AdminKeyOps = NewCounterVec(
		"leyline_admin_key_ops_total",
		"Admin API key mutations. op ∈ {create,delete,update_role}.",
		"vault", "op",
	)
	PanicsRecovered = NewCounter(
		"leyline_panics_recovered_total",
		"HTTP handler panics caught by httpx.Recover (excludes http.ErrAbortHandler).",
	)
)

// processStartTime is captured at package init. main() can override via
// SetProcessStartTime if it wants a different anchor; the init-time value
// is correct for the common case.
var processStartTime = time.Now().Unix()

// SetProcessStartTime overrides the timestamp emitted as
// process_start_time_seconds. Intended for main() to call once before any
// scrape, if it wants a tighter anchor than the metrics-package init time.
func SetProcessStartTime(t time.Time) { processStartTime = t.Unix() }

// RegisterRuntimeMetrics registers the standard runtime gauges (go_goroutines,
// go_memstats_*, process_start_time_seconds). Called by main(); split out so
// tests can use a fresh Default without these polluting output.
func RegisterRuntimeMetrics() {
	RegisterGaugeFunc(
		"go_goroutines",
		"Number of goroutines that currently exist.",
		nil,
		func(emit func([]string, int64)) { emit(nil, int64(runtime.NumGoroutine())) },
	)
	RegisterGaugeFunc(
		"go_memstats_heap_alloc_bytes",
		"Bytes currently allocated on the heap.",
		nil,
		func(emit func([]string, int64)) {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			emit(nil, int64(m.HeapAlloc))
		},
	)
	RegisterGaugeFunc(
		"go_memstats_sys_bytes",
		"Total bytes obtained from system.",
		nil,
		func(emit func([]string, int64)) {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			emit(nil, int64(m.Sys))
		},
	)
	RegisterGaugeFunc(
		"go_gc_duration_seconds_sum",
		"Cumulative seconds spent in GC pauses.",
		nil,
		func(emit func([]string, int64)) {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			emit(nil, int64(m.PauseTotalNs/1e9))
		},
	)
	RegisterGaugeFunc(
		"go_gc_duration_seconds_count",
		"Number of completed GC cycles.",
		nil,
		func(emit func([]string, int64)) {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			emit(nil, int64(m.NumGC))
		},
	)
	RegisterGaugeFunc(
		"process_start_time_seconds",
		"Process start time as a Unix timestamp (seconds since epoch).",
		nil,
		func(emit func([]string, int64)) { emit(nil, processStartTime) },
	)
}

// RegisterActiveClients wires the per-vault client-count gauge. snapshot is
// invoked once per scrape and returns a fresh map of vaultID → client count.
func RegisterActiveClients(snapshot func() map[string]int) {
	RegisterGaugeFunc(
		"leyline_active_clients",
		"Active WebSocket clients per vault.",
		[]string{"vault"},
		func(emit func([]string, int64)) {
			for vault, n := range snapshot() {
				emit([]string{vault}, int64(n))
			}
		},
	)
}

// RegisterVaultsHydrated wires the global hydrated-vault count gauge.
func RegisterVaultsHydrated(count func() int) {
	RegisterGaugeFunc(
		"leyline_vaults_hydrated",
		"Number of vaults currently hydrated in the hub.",
		nil,
		func(emit func([]string, int64)) { emit(nil, int64(count())) },
	)
}
