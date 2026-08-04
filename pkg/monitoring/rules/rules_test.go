package rules

import (
	"testing"
)

func TestSetupRules(t *testing.T) {
	if err := SetupRules(); err != nil {
		t.Fatalf("SetupRules() error: %v", err)
	}

	alerts := ListAlerts()
	if len(alerts) == 0 {
		t.Fatal("expected registered alerts, got none")
	}

	expectedAlerts := map[string]bool{
		"VMFileRestoreOperatorDown":    false,
		"VMFileRestoreHighFailureRate": false,
	}

	for _, alert := range alerts {
		if _, ok := expectedAlerts[alert.Alert]; ok {
			expectedAlerts[alert.Alert] = true
		}
	}

	for name, found := range expectedAlerts {
		if !found {
			t.Errorf("expected alert %q not registered", name)
		}
	}
}

func TestAlertLabels(t *testing.T) {
	if err := SetupRules(); err != nil {
		t.Fatalf("SetupRules() error: %v", err)
	}

	for _, alert := range ListAlerts() {
		t.Run(alert.Alert, func(t *testing.T) {
			if alert.Labels["kubernetes_operator_part_of"] != "kubevirt" {
				t.Error("missing kubernetes_operator_part_of=kubevirt label")
			}
			if alert.Labels["kubernetes_operator_component"] != "vm-file-restore-operator" {
				t.Error("missing kubernetes_operator_component label")
			}
			if alert.Labels["severity"] == "" {
				t.Error("missing severity label")
			}
			if alert.Annotations["runbook_url"] == "" {
				t.Error("missing runbook_url annotation")
			}
		})
	}
}

func TestBuildPrometheusRule(t *testing.T) {
	if err := SetupRules(); err != nil {
		t.Fatalf("SetupRules() error: %v", err)
	}

	rule, err := BuildPrometheusRule("test-namespace")
	if err != nil {
		t.Fatalf("BuildPrometheusRule() error: %v", err)
	}

	if rule.Name != RuleName {
		t.Errorf("PrometheusRule name = %q, want %q", rule.Name, RuleName)
	}
	if rule.Namespace != "test-namespace" {
		t.Errorf("PrometheusRule namespace = %q, want %q", rule.Namespace, "test-namespace")
	}
	if len(rule.Spec.Groups) == 0 {
		t.Error("expected at least one rule group")
	}
}
