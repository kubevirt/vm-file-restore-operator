package metrics

import (
	runtimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/rhobs/operator-observability-toolkit/pkg/operatormetrics"
)

func SetupMetrics() error {
	operatormetrics.Register = runtimemetrics.Registry.Register
	return operatormetrics.RegisterMetrics(filerestoreMetrics)
}

func ListMetrics() []operatormetrics.Metric {
	return operatormetrics.ListMetrics()
}
