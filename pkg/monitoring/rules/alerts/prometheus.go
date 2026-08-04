package alerts

import (
	"fmt"

	"github.com/rhobs/operator-observability-toolkit/pkg/operatorrules"
)

const (
	runbookURLTemplate = "https://kubevirt.io/monitoring/runbooks/%s"
)

func Register(registry *operatorrules.Registry) error {
	alerts := operatorAlerts()
	for i := range alerts {
		alert := &alerts[i]
		alert.Labels["kubernetes_operator_part_of"] = "kubevirt"
		alert.Labels["kubernetes_operator_component"] = "vm-file-restore-operator"
		alert.Annotations["runbook_url"] = fmt.Sprintf(runbookURLTemplate, alert.Alert)
	}
	return registry.RegisterAlerts(alerts)
}
