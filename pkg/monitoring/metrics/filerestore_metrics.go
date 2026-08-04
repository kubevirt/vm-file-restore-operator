package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rhobs/operator-observability-toolkit/pkg/operatormetrics"
)

var (
	restoresTotal = operatormetrics.NewCounterVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmfr_restores_total",
			Help: "Total number of VirtualMachineFileRestore operations completed.",
		},
		[]string{"result"},
	)

	restoresInProgress = operatormetrics.NewGauge(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmfr_restores_in_progress",
			Help: "Number of VirtualMachineFileRestore operations currently in progress.",
		},
	)

	restoreDurationSeconds = operatormetrics.NewHistogramVec(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmfr_restore_duration_seconds",
			Help: "Duration of VirtualMachineFileRestore operations in seconds.",
		},
		prometheus.HistogramOpts{
			Buckets: []float64{10, 30, 60, 120, 300, 600},
		},
		[]string{"result"},
	)

	operatorUp = operatormetrics.NewGauge(
		operatormetrics.MetricOpts{
			Name: "kubevirt_vmfr_operator_up",
			Help: "Set to 1 when the vm-file-restore-operator is running.",
		},
	)

	filerestoreMetrics = []operatormetrics.Metric{
		restoresTotal,
		restoresInProgress,
		restoreDurationSeconds,
		operatorUp,
	}
)

func IncRestoresTotal(result string) {
	restoresTotal.WithLabelValues(result).Inc()
}

func IncRestoresInProgress() {
	restoresInProgress.Inc()
}

func DecRestoresInProgress() {
	restoresInProgress.Dec()
}

func ObserveRestoreDuration(result string, duration time.Duration) {
	restoreDurationSeconds.WithLabelValues(result).Observe(duration.Seconds())
}

func SetOperatorUp() {
	operatorUp.Set(1)
}
