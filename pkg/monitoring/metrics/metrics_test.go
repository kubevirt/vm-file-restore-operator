package metrics

import (
	"os"
	"testing"
	"time"

	"github.com/rhobs/operator-observability-toolkit/pkg/operatormetrics"
)

func TestMain(m *testing.M) {
	if err := SetupMetrics(); err != nil {
		panic("SetupMetrics() failed: " + err.Error())
	}
	os.Exit(m.Run())
}

func TestSetupMetrics(t *testing.T) {
	metrics := ListMetrics()
	if len(metrics) == 0 {
		t.Fatal("expected registered metrics, got none")
	}

	expectedNames := map[string]bool{
		"kubevirt_vmfr_restores_total":           false,
		"kubevirt_vmfr_restores_in_progress":     false,
		"kubevirt_vmfr_restore_duration_seconds": false,
		"kubevirt_vmfr_operator_up":              false,
	}

	for _, m := range metrics {
		name := m.GetOpts().Name
		if _, ok := expectedNames[name]; ok {
			expectedNames[name] = true
		}
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("expected metric %q not registered", name)
		}
	}
}

func TestMetricTypes(t *testing.T) {
	tests := []struct {
		name       string
		metricName string
		wantType   operatormetrics.MetricType
	}{
		{"restores_total is CounterVec", "kubevirt_vmfr_restores_total", operatormetrics.CounterVecType},
		{"restores_in_progress is Gauge", "kubevirt_vmfr_restores_in_progress", operatormetrics.GaugeType},
		{"restore_duration_seconds is HistogramVec",
			"kubevirt_vmfr_restore_duration_seconds", operatormetrics.HistogramVecType},
		{"operator_up is Gauge", "kubevirt_vmfr_operator_up", operatormetrics.GaugeType},
	}

	metrics := ListMetrics()
	metricsByName := map[string]operatormetrics.Metric{}
	for _, m := range metrics {
		metricsByName[m.GetOpts().Name] = m
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := metricsByName[tt.metricName]
			if !ok {
				t.Fatalf("metric %q not found", tt.metricName)
			}
			if m.GetType() != tt.wantType {
				t.Errorf("metric %q type = %v, want %v", tt.metricName, m.GetType(), tt.wantType)
			}
		})
	}
}

func TestSetterHelpers(t *testing.T) {
	IncRestoresTotal("succeeded")
	IncRestoresTotal("failed")
	IncRestoresInProgress()
	DecRestoresInProgress()
	ObserveRestoreDuration("succeeded", 30*time.Second)
	SetOperatorUp()
}
