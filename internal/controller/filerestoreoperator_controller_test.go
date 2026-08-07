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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	restorev1alpha1 "kubevirt.io/vm-file-restore-operator/api/v1alpha1"
)

var _ = Describe("FileRestoreOperator Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		filerestoreoperator := &restorev1alpha1.FileRestoreOperator{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind FileRestoreOperator")
			err := k8sClient.Get(ctx, typeNamespacedName, filerestoreoperator)
			if err != nil && errors.IsNotFound(err) {
				resource := &restorev1alpha1.FileRestoreOperator{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: restorev1alpha1.FileRestoreOperatorSpec{
						ImagePullPolicy: corev1.PullIfNotPresent,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &restorev1alpha1.FileRestoreOperator{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FileRestoreOperator")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, filerestoreoperator)
				if err != nil {
					return false
				}
				return apimeta.IsStatusConditionTrue(filerestoreoperator.Status.Conditions, restorev1alpha1.ConditionAvailable)
			}, time.Second*10, time.Millisecond*250).Should(BeTrue())

			Expect(filerestoreoperator.Status.ObservedGeneration).To(Equal(filerestoreoperator.Generation))
		})

		It("should handle NotFound resource gracefully", func() {
			By("Reconciling a non-existent resource")
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should be idempotent when already reconciled", func() {
			By("Creating a FileRestoreOperator resource")
			resource := &restorev1alpha1.FileRestoreOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-idempotency",
					Namespace: "default",
				},
				Spec: restorev1alpha1.FileRestoreOperatorSpec{},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			})

			controllerReconciler := &FileRestoreOperatorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconcile
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-idempotency",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get current state
			var firstReconcile restorev1alpha1.FileRestoreOperator
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-idempotency",
				Namespace: "default",
			}, &firstReconcile)).To(Succeed())

			firstGeneration := firstReconcile.Status.ObservedGeneration
			Expect(apimeta.IsStatusConditionTrue(firstReconcile.Status.Conditions, restorev1alpha1.ConditionAvailable)).To(BeTrue())

			// Second reconcile without changes - should be no-op
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-idempotency",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify status unchanged
			var secondReconcile restorev1alpha1.FileRestoreOperator
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-idempotency",
				Namespace: "default",
			}, &secondReconcile)).To(Succeed())

			Expect(apimeta.IsStatusConditionTrue(secondReconcile.Status.Conditions, restorev1alpha1.ConditionAvailable)).To(BeTrue())
			Expect(secondReconcile.Status.ObservedGeneration).To(Equal(firstGeneration))
		})
	})
})
