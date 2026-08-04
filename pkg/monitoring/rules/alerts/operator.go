package alerts

import (
	promv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func operatorAlerts() []promv1.Rule {
	return []promv1.Rule{
		{
			Alert: "VMFileRestoreOperatorDown",
			Expr:  intstr.FromString("kubevirt_vmfr_operator_up == 0"),
			For:   ptr.To[promv1.Duration]("5m"),
			Annotations: map[string]string{
				"summary": "The vm-file-restore-operator is down.",
			},
			Labels: map[string]string{
				"severity":               "critical",
				"operator_health_impact": "critical",
			},
		},
		{
			Alert: "VMFileRestoreHighFailureRate",
			Expr:  intstr.FromString("rate(kubevirt_vmfr_restores_total{result=\"failed\"}[1h]) > 0.5"),
			For:   ptr.To[promv1.Duration]("15m"),
			Annotations: map[string]string{
				"summary": "VirtualMachineFileRestore operations are failing at a high rate.",
			},
			Labels: map[string]string{
				"severity":               "warning",
				"operator_health_impact": "warning",
			},
		},
	}
}
