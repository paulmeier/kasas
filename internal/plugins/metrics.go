package plugins

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var pluginInvocations = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_plugin_invocations_total",
	Help: "Total plugin hook invocations, labelled by plugin and hook.",
}, []string{"plugin", "hook"})

var pluginErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_plugin_errors_total",
	Help: "Total plugin hook invocations that returned an error, labelled by plugin.",
}, []string{"plugin"})

var pluginJobsDropped = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_plugin_jobs_dropped_total",
	Help: "Total plugin hook jobs dropped because the plugin's queue was full.",
}, []string{"plugin"})
