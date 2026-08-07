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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

var _ = Describe("ForgejoActionSecret Controller", func() {
	Context("When reconciling a ForgejoActionSecret against a real server", func() {
		const instanceName = "forgejo-actionsecret-test-instance"
		const authSecretName = "forgejo-actionsecret-test-creds"
		const orgName = "actionsecret-org"
		const resourceName = "docker-password"
		const valueSecretName = "docker-password-value"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoActionSecretReconciler
			directClient         *forgejoapi.Client
		)

		hasActionSecret := func(name string) bool {
			secrets, err := directClient.ListOrgActionSecrets(ctx, orgName)
			Expect(err).NotTo(HaveOccurred())
			for _, s := range secrets {
				if s.Name == name {
					return true
				}
			}
			return false
		}

		BeforeEach(func() {
			controllerReconciler = &ForgejoActionSecretReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			directClient = forgejoapi.New(forgejoBaseURL, forgejoAdminUsername, forgejoAdminPassword)

			By("creating the Forgejo instance credentials secret")
			authSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: authSecretName, Namespace: "default"},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

			By("creating the Forgejo instance")
			instance := &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: instanceName},
				Spec: forgejoschrankedev1alpha1.ForgejoSpec{
					Url: forgejoBaseURL,
					AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
						SecretName:      authSecretName,
						SecretNamespace: "default",
						UsernameKey:     "username",
						PasswordKey:     "password",
					},
				},
			}
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())

			By("creating and reconciling the organization the action secret belongs to")
			org := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: orgName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
				},
			}
			Expect(k8sClient.Create(ctx, org)).To(Succeed())
			orgReconciler := &ForgejoOrganizationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := orgReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: orgName}})
			Expect(err).NotTo(HaveOccurred())

			By("creating the Kubernetes secret holding the action secret's value")
			valueSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: valueSecretName, Namespace: "default"},
				StringData: map[string]string{"value": "hunter2"},
			}
			Expect(k8sClient.Create(ctx, valueSecret)).To(Succeed())
		})

		AfterEach(func() {
			By("deleting the ForgejoActionSecret and letting the finalizer run")
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: valueSecretName, Namespace: "default"},
			})

			orgReconciler := &ForgejoOrganizationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			org := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: orgName}, org); err == nil {
				Expect(k8sClient.Delete(ctx, org)).To(Succeed())
				_, err := orgReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: orgName}})
				Expect(err).NotTo(HaveOccurred())
			}

			_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: instanceName},
			})
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: authSecretName, Namespace: "default"},
			})
		})

		It("creates a real action secret on the organization and sets a finalizer", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Name:            "DOCKER_PASSWORD",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "value",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoActionSecret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, actionSecretFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the secret really exists on the Forgejo server (its value can't be read back)")
			Expect(hasActionSecret("DOCKER_PASSWORD")).To(BeTrue())
		})

		It("is idempotent when reconciled again after the value changes", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Name:            "DOCKER_PASSWORD",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "value",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("changing the source Kubernetes secret's value and reconciling again")
			valueSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: valueSecretName, Namespace: "default"}, valueSecret)).To(Succeed())
			valueSecret.Data["value"] = []byte("hunter3")
			Expect(k8sClient.Update(ctx, valueSecret)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(hasActionSecret("DOCKER_PASSWORD")).To(BeTrue())
		})

		It("uppercases the secret name on Forgejo regardless of the case used in spec.name", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Name:            "docker_password",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "value",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(hasActionSecret("DOCKER_PASSWORD")).To(BeTrue())
			Expect(hasActionSecret("docker_password")).To(BeFalse())
		})

		It("reports an Error status when the referenced ForgejoOrganization does not exist", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: "does-not-exist"},
					Name:            "DOCKER_PASSWORD",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "value",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoActionSecret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
		})

		It("reports an Error status when the referenced Kubernetes secret key is missing", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Name:            "DOCKER_PASSWORD",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "does-not-exist",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoActionSecret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
		})

		It("deletes the real action secret from Forgejo when the CR is deleted", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoActionSecret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoActionSecretSpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Name:            "DOCKER_PASSWORD",
					ValueFrom: forgejoschrankedev1alpha1.ValueFromSecret{
						SecretName:      valueSecretName,
						SecretNamespace: "default",
						Key:             "value",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(hasActionSecret("DOCKER_PASSWORD")).To(BeTrue())

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling again to let the finalizer run and remove the action secret")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, typeNamespacedName, &forgejoschrankedev1alpha1.ForgejoActionSecret{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())

			Expect(hasActionSecret("DOCKER_PASSWORD")).To(BeFalse())
		})
	})
})
