package rules

import (
	promv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/rhobs/operator-observability-toolkit/pkg/operatorrules"

	"kubevirt.io/vm-file-restore-operator/pkg/monitoring/rules/alerts"
)

const (
	RuleName = "prometheus-vm-file-restore-rules"
)

var operatorRegistry = operatorrules.NewRegistry()

func SetupRules() error {
	return alerts.Register(operatorRegistry)
}

func BuildPrometheusRule(namespace string) (*promv1.PrometheusRule, error) {
	return operatorRegistry.BuildPrometheusRule(
		RuleName,
		namespace,
		map[string]string{
			"filerestore.kubevirt.io":            "",
			"prometheus.filerestore.kubevirt.io": "true",
		},
	)
}

func ListAlerts() []promv1.Rule {
	return operatorRegistry.ListAlerts()
}
