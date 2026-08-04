package controller

import (
	"context"
	"fmt"

	promv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"kubevirt.io/vm-file-restore-operator/pkg/monitoring/rules"
)

const (
	monitoringLabel     = "prometheus.filerestore.kubevirt.io"
	serviceMonitorName  = "service-monitor-vm-file-restore"
	prometheusRoleName  = "vm-file-restore-monitoring"
	metricsServiceName  = "vm-file-restore-prometheus-metrics"
	metricsPortName     = "metrics"
	metricsPort         = 8443
	prometheusK8sSA     = "prometheus-k8s"
	monitoringNamespace = "openshift-monitoring"
)

var commonLabels = map[string]string{
	"filerestore.kubevirt.io": "",
	monitoringLabel:           "true",
}

func newServiceMonitor(namespace string) *promv1.ServiceMonitor {
	return &promv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceMonitorName,
			Namespace: namespace,
			Labels: map[string]string{
				"filerestore.kubevirt.io":         "",
				monitoringLabel:                   "true",
				"openshift.io/cluster-monitoring": "",
			},
		},
		Spec: promv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					monitoringLabel: "true",
				},
			},
			NamespaceSelector: promv1.NamespaceSelector{
				MatchNames: []string{namespace},
			},
			Endpoints: []promv1.Endpoint{
				{
					HonorLabels: true,
					Port:        metricsPortName,
					Scheme:      "https",
					TLSConfig: &promv1.TLSConfig{
						SafeTLSConfig: promv1.SafeTLSConfig{
							InsecureSkipVerify: true,
						},
					},
				},
			},
		},
	}
}

func newPrometheusService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      metricsServiceName,
			Namespace: namespace,
			Labels:    commonLabels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"control-plane":          "controller-manager",
				"app.kubernetes.io/name": "vm-file-restore-operator",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       metricsPortName,
					Port:       int32(metricsPort),
					TargetPort: intstr.FromInt32(int32(metricsPort)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func newPrometheusRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusRoleName,
			Namespace: namespace,
			Labels:    commonLabels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"services", "endpoints", "pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func newPrometheusRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prometheusRoleName,
			Namespace: namespace,
			Labels:    commonLabels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     prometheusRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      prometheusK8sSA,
				Namespace: monitoringNamespace,
			},
		},
	}
}

func ensurePrometheusResources(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner metav1.Object, namespace string) error {
	logger := log.FromContext(ctx)

	prometheusRule, err := rules.BuildPrometheusRule(namespace)
	if err != nil {
		return fmt.Errorf("failed to build prometheus rule: %w", err)
	}

	resources := []client.Object{
		newPrometheusService(namespace),
		newPrometheusRole(namespace),
		newPrometheusRoleBinding(namespace),
		newServiceMonitor(namespace),
		prometheusRule,
	}

	for _, resource := range resources {
		if err := controllerutil.SetOwnerReference(owner, resource, scheme); err != nil {
			return fmt.Errorf("failed to set owner reference on %s: %w", resource.GetName(), err)
		}

		existing := resource.DeepCopyObject().(client.Object)
		err := c.Get(ctx, client.ObjectKeyFromObject(resource), existing)
		if err != nil {
			if errors.IsNotFound(err) {
				if createErr := c.Create(ctx, resource); createErr != nil {
					if !errors.IsAlreadyExists(createErr) {
						return fmt.Errorf("failed to create %s %s: %w", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName(), createErr)
					}
				} else {
					logger.Info("Created monitoring resource", "kind", resource.GetObjectKind().GroupVersionKind().Kind, "name", resource.GetName())
				}
			} else {
				return fmt.Errorf("failed to get %s %s: %w", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName(), err)
			}
		} else {
			resource.SetResourceVersion(existing.GetResourceVersion())
			if updateErr := c.Update(ctx, resource); updateErr != nil {
				return fmt.Errorf("failed to update %s %s: %w", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName(), updateErr)
			}
		}
	}

	return nil
}
