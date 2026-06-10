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

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"kubevirt.io/vm-file-restore-operator/test/utils"
)

// TestE2E runs the end-to-end (e2e) test suite for the project.
// These tests run against a kubevirtci cluster with the operator already deployed.
// Prerequisites:
//   - kubevirtci cluster running (make cluster-up)
//   - Operator deployed (make cluster-sync)
//   - KUBECONFIG pointing to the cluster
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting vm-file-restore-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	// E2E tests assume the operator is already deployed to the kubevirtci cluster.
	// Use 'make cluster-sync' to build and deploy the operator before running tests.

	By("verifying KUBECONFIG is set")
	kubeconfig := os.Getenv("KUBECONFIG")
	Expect(kubeconfig).NotTo(BeEmpty(), "KUBECONFIG must be set to point to kubevirtci cluster")
	_, _ = fmt.Fprintf(GinkgoWriter, "Using KUBECONFIG: %s\n", kubeconfig)

	By("verifying operator is deployed")
	cmd := exec.Command("kubectl", "get", "deployment", "-n", "file-restore", "vm-file-restore-operator")
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Operator deployment not found. Run 'make cluster-sync' to deploy the operator first.")
})

var _ = AfterSuite(func() {
	// Nothing to clean up - operator stays running for debugging
})
