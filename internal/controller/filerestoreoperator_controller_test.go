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

var _ = Describe("operatorVersion", func() {
	It("returns the struct field when set", func() {
		r := &FileRestoreOperatorReconciler{OperatorVersion: "1.2.3"}
		Expect(r.operatorVersion()).To(Equal("1.2.3"))
	})

	It("returns 'devel' when struct field is empty", func() {
		r := &FileRestoreOperatorReconciler{}
		Expect(r.operatorVersion()).To(Equal("devel"))
	})
})

var _ = Describe("FileRestoreOperator Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const testVersion = "0.0.1-test"

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

		It("should populate version fields and status conditions", func() {
			By("Reconciling the created resource")
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				OperatorVersion: testVersion,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, filerestoreoperator)).To(Succeed())
				g.Expect(filerestoreoperator.Status.ObservedGeneration).To(Equal(filerestoreoperator.Generation))
				g.Expect(filerestoreoperator.Status.OperatorVersion).To(Equal(testVersion))
				g.Expect(filerestoreoperator.Status.TargetVersion).To(Equal(testVersion))
				g.Expect(filerestoreoperator.Status.ObservedVersion).To(Equal(testVersion))

				available := apimeta.FindStatusCondition(filerestoreoperator.Status.Conditions, restorev1alpha1.ConditionAvailable)
				g.Expect(available).NotTo(BeNil())
				g.Expect(available.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(available.Reason).To(Equal("Deployed"))
				g.Expect(available.Message).To(Equal("FileRestoreOperator is available"))
				g.Expect(available.ObservedGeneration).To(Equal(filerestoreoperator.Generation))

				progressing := apimeta.FindStatusCondition(filerestoreoperator.Status.Conditions, restorev1alpha1.ConditionProgressing)
				g.Expect(progressing).NotTo(BeNil())
				g.Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(progressing.Reason).To(Equal("Deployed"))
				g.Expect(progressing.Message).To(Equal("FileRestoreOperator is not progressing"))
				g.Expect(progressing.ObservedGeneration).To(Equal(filerestoreoperator.Generation))

				degraded := apimeta.FindStatusCondition(filerestoreoperator.Status.Conditions, restorev1alpha1.ConditionDegraded)
				g.Expect(degraded).NotTo(BeNil())
				g.Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(degraded.Reason).To(Equal("Deployed"))
				g.Expect(degraded.Message).To(Equal("FileRestoreOperator is not degraded"))
				g.Expect(degraded.ObservedGeneration).To(Equal(filerestoreoperator.Generation))

				upgradeable := apimeta.FindStatusCondition(filerestoreoperator.Status.Conditions, restorev1alpha1.ConditionUpgradeable)
				g.Expect(upgradeable).NotTo(BeNil())
				g.Expect(upgradeable.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(upgradeable.Reason).To(Equal("Deployed"))
				g.Expect(upgradeable.Message).To(Equal("FileRestoreOperator is upgradeable"))
				g.Expect(upgradeable.ObservedGeneration).To(Equal(filerestoreoperator.Generation))
			}, time.Second*10, time.Millisecond*250).Should(Succeed())
		})

		It("should handle NotFound resource gracefully", func() {
			By("Reconciling a non-existent resource")
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				OperatorVersion: testVersion,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should report 'devel' version when OperatorVersion is unset", func() {
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, filerestoreoperator)).To(Succeed())
				g.Expect(filerestoreoperator.Status.OperatorVersion).To(Equal("devel"))
				g.Expect(filerestoreoperator.Status.TargetVersion).To(Equal("devel"))
				g.Expect(filerestoreoperator.Status.ObservedVersion).To(Equal("devel"))
			}, time.Second*10, time.Millisecond*250).Should(Succeed())
		})

		It("should update status when operator version changes", func() {
			By("Reconciling at the initial version")
			controllerReconciler := &FileRestoreOperatorReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				OperatorVersion: testVersion,
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				var after1 restorev1alpha1.FileRestoreOperator
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, &after1)).To(Succeed())
				g.Expect(after1.Status.OperatorVersion).To(Equal(testVersion))
			}, time.Second*10, time.Millisecond*250).Should(Succeed())

			By("Reconciling after an operator version bump")
			newVersion := testVersion + "-v2"
			controllerReconciler2 := &FileRestoreOperatorReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				OperatorVersion: newVersion,
			}
			_, err = controllerReconciler2.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, typeNamespacedName, filerestoreoperator)).To(Succeed())
				g.Expect(filerestoreoperator.Status.OperatorVersion).To(Equal(newVersion))
				g.Expect(filerestoreoperator.Status.TargetVersion).To(Equal(newVersion))
				g.Expect(filerestoreoperator.Status.ObservedVersion).To(Equal(newVersion))
			}, time.Second*10, time.Millisecond*250).Should(Succeed())
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
				err := k8sClient.Delete(ctx, resource)
				if err != nil && !errors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}
			})

			controllerReconciler := &FileRestoreOperatorReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				OperatorVersion: testVersion,
			}

			nn := types.NamespacedName{Name: "test-idempotency", Namespace: "default"}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var firstReconcile restorev1alpha1.FileRestoreOperator
			Expect(k8sClient.Get(ctx, nn, &firstReconcile)).To(Succeed())
			firstGeneration := firstReconcile.Status.ObservedGeneration
			Expect(firstReconcile.Status.ObservedVersion).To(Equal(testVersion))
			Expect(apimeta.IsStatusConditionTrue(firstReconcile.Status.Conditions, restorev1alpha1.ConditionAvailable)).To(BeTrue())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			var secondReconcile restorev1alpha1.FileRestoreOperator
			Expect(k8sClient.Get(ctx, nn, &secondReconcile)).To(Succeed())
			Expect(apimeta.IsStatusConditionTrue(secondReconcile.Status.Conditions, restorev1alpha1.ConditionAvailable)).To(BeTrue())
			Expect(apimeta.IsStatusConditionFalse(secondReconcile.Status.Conditions, restorev1alpha1.ConditionProgressing)).To(BeTrue())
			Expect(apimeta.IsStatusConditionFalse(secondReconcile.Status.Conditions, restorev1alpha1.ConditionDegraded)).To(BeTrue())
			Expect(apimeta.IsStatusConditionTrue(secondReconcile.Status.Conditions, restorev1alpha1.ConditionUpgradeable)).To(BeTrue())
			Expect(secondReconcile.Status.ObservedGeneration).To(Equal(firstGeneration))
			Expect(secondReconcile.Status.OperatorVersion).To(Equal(testVersion))
			Expect(secondReconcile.Status.TargetVersion).To(Equal(testVersion))
			Expect(secondReconcile.Status.ObservedVersion).To(Equal(testVersion))
		})
	})
})
