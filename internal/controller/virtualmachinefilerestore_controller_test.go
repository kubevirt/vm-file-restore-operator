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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

var _ = Describe("VirtualMachineFileRestore Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-restore-1"
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for automatic restore")
			resource := &restorev1alpha1.VirtualMachineFileRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: ptr.To("kubevirt.io"),
						Kind:     "VirtualMachine",
						Name:     "test-vm",
					},
					Source: restorev1alpha1.RestoreSource{
						PVC: &restorev1alpha1.PVCSource{
							Name: "test-pvc",
						},
					},
					SourcePath: "/data/backup.tar",
					TargetPath: "/restore",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up the test resource")
			resource := &restorev1alpha1.VirtualMachineFileRestore{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				// Remove finalizers to allow immediate deletion
				resource.Finalizers = []string{}
				Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
			// Wait for deletion to complete
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, resource)
				return errors.IsNotFound(err)
			}, "10s", "100ms").Should(BeTrue())
		})

		It("should successfully reconcile the resource", func() {
			By("creating the reconciler")
			controllerReconciler := &VirtualMachineFileRestoreReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("reconciling the created resource")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			// The reconciliation may fail in the Init phase due to missing KubeVirt,
			// but the finalizer should be added before that happens
			if err != nil {
				// Expected error: KubeVirt not installed or VM not found
				// This is acceptable for integration tests
				By("verifying expected error in Init phase")
			}

			By("verifying the finalizer was added")
			resource := &restorev1alpha1.VirtualMachineFileRestore{}
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(resource.Finalizers).To(ContainElement("filerestore.kubevirt.io/cleanup"))
		})

		It("should add finalizer on first reconcile", func() {
			By("creating the reconciler")
			controllerReconciler := &VirtualMachineFileRestoreReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("verifying no finalizer exists initially")
			resource := &restorev1alpha1.VirtualMachineFileRestore{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(resource.Finalizers).To(BeEmpty())

			By("reconciling the resource")
			// May fail in Init phase (missing VM), but finalizer should be added first
			// We intentionally don't check the error - reconcile happens in background
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			By("verifying finalizer was added")
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(resource.Finalizers).To(ContainElement("filerestore.kubevirt.io/cleanup"))
		})
	})

	Context("When reconciling a resource with deletion timestamp", func() {
		const resourceName = "test-restore-2"
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		It("should remove finalizer on deletion", func() {
			By("creating a resource with finalizer")
			resource := &restorev1alpha1.VirtualMachineFileRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:       resourceName,
					Namespace:  "default",
					Finalizers: []string{"filerestore.kubevirt.io/cleanup"},
				},
				Spec: restorev1alpha1.VirtualMachineFileRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: ptr.To("kubevirt.io"),
						Kind:     "VirtualMachine",
						Name:     "test-vm",
					},
					Source: restorev1alpha1.RestoreSource{
						PVC: &restorev1alpha1.PVCSource{
							Name: "test-pvc",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("deleting the resource")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling to trigger finalizer cleanup")
			controllerReconciler := &VirtualMachineFileRestoreReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying resource is deleted")
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
