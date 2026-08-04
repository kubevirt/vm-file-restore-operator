/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import "os"

const (
	defaultOperatorNamespace    = "file-restore"
	defaultOperatorDeployment   = "vm-file-restore-operator"
	defaultOperatorSSHConfigMap = "vm-file-restore-operator-ssh"
	defaultServiceAccountName   = "vm-file-restore-controller-manager"
	defaultMetricsServiceName   = "vm-file-restore-controller-manager-metrics-service"
	defaultFileRestoreOperator  = "vm-file-restore-operator"
)

// operatorNamespace returns the namespace where the operator is deployed.
// Override with OPERATOR_NAMESPACE for QE / alternative installs.
func operatorNamespace() string {
	return envOrDefault("OPERATOR_NAMESPACE", defaultOperatorNamespace)
}

func operatorDeploymentName() string {
	return envOrDefault("OPERATOR_DEPLOYMENT", defaultOperatorDeployment)
}

func operatorSSHConfigMapName() string {
	return envOrDefault("OPERATOR_SSH_CONFIGMAP", defaultOperatorSSHConfigMap)
}

func operatorServiceAccountName() string {
	return envOrDefault("OPERATOR_SERVICE_ACCOUNT", defaultServiceAccountName)
}

func operatorMetricsServiceName() string {
	return envOrDefault("OPERATOR_METRICS_SERVICE", defaultMetricsServiceName)
}

func fileRestoreOperatorCRName() string {
	return envOrDefault("FILE_RESTORE_OPERATOR_NAME", defaultFileRestoreOperator)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
