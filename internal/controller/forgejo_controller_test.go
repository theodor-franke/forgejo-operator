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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

var _ = Describe("Forgejo Controller", func() {
	Context("When reconciling a Forgejo instance against a real server", func() {
		const resourceName = "forgejo-controller-test-instance"
		const secretName = "forgejo-controller-test-creds"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: resourceName}

		var controllerReconciler *ForgejoReconciler

		BeforeEach(func() {
			controllerReconciler = &ForgejoReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("creating the credentials secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
				},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up the Forgejo resource and its secret")
			_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			})
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			})
		})

		It("calls the real Forgejo info endpoint and records the reported version", func() {
			By("creating the Forgejo resource pointing at the test container")
			resource := &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: forgejoschrankedev1alpha1.ForgejoSpec{
					Url: forgejoBaseURL,
					AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
						SecretName:      secretName,
						SecretNamespace: "default",
						UsernameKey:     "username",
						PasswordKey:     "password",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("reconciling")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("independently querying the real server for its version")
			directClient := forgejoapi.New(forgejoBaseURL, forgejoAdminUsername, forgejoAdminPassword)
			expectedVersion, err := directClient.GetVersion(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(expectedVersion).NotTo(BeEmpty())

			By("checking the recorded status matches the real server")
			updated := &forgejoschrankedev1alpha1.Forgejo{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Ready"))
			Expect(updated.Status.Version).To(Equal(expectedVersion))
			Expect(updated.Status.Message).To(BeEmpty())
		})

		It("reports an Error status when the credentials are rejected by the real server", func() {
			By("pointing the secret at invalid credentials")
			badSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: "default"}, badSecret)).To(Succeed())
			badSecret.Data["password"] = []byte("definitely-not-the-password")
			Expect(k8sClient.Update(ctx, badSecret)).To(Succeed())

			resource := &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: forgejoschrankedev1alpha1.ForgejoSpec{
					Url: forgejoBaseURL,
					AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
						SecretName:      secretName,
						SecretNamespace: "default",
						UsernameKey:     "username",
						PasswordKey:     "password",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.Forgejo{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
		})

		It("ignores resources that no longer exist", func() {
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist"},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("generates and creates the default pull key secret when it doesn't exist", func() {
			const pullKeySecretName = "forgejo-controller-test-pull-key"

			resource := &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: forgejoschrankedev1alpha1.ForgejoSpec{
					Url: forgejoBaseURL,
					AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
						SecretName:      secretName,
						SecretNamespace: "default",
						UsernameKey:     "username",
						PasswordKey:     "password",
					},
					DefaultPullKeySecretRef: &forgejoschrankedev1alpha1.PullKeySecretRef{
						SecretName:      pullKeySecretName,
						SecretNamespace: "default",
					},
					SSHHost: forgejoSSHHost,
					SSHPort: forgejoSSHPort,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: pullKeySecretName, Namespace: "default"},
				})
			}()

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.Forgejo{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the generated secret has a real, usable keypair and known_hosts entry")
			pullKeySecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pullKeySecretName, Namespace: "default"}, pullKeySecret)).To(Succeed())
			Expect(string(pullKeySecret.Data["publicKey"])).To(HavePrefix("ssh-ed25519 "))
			Expect(string(pullKeySecret.Data["identity.pub"])).To(Equal(string(pullKeySecret.Data["publicKey"])))
			Expect(string(pullKeySecret.Data["identity"])).To(ContainSubstring("BEGIN OPENSSH PRIVATE KEY"))
			Expect(string(pullKeySecret.Data["known_hosts"])).To(ContainSubstring(forgejoSSHHost))

			By("reconciling again and checking the existing secret is left untouched")
			firstResourceVersion := pullKeySecret.ResourceVersion
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pullKeySecretName, Namespace: "default"}, pullKeySecret)).To(Succeed())
			Expect(pullKeySecret.ResourceVersion).To(Equal(firstResourceVersion))
		})
	})
})

var _ = Describe("Forgejo resource scope", func() {
	It("is cluster-scoped, not namespaced", func() {
		ctx := context.Background()
		resource := &forgejoschrankedev1alpha1.Forgejo{
			ObjectMeta: metav1.ObjectMeta{Name: "forgejo-scope-check"},
			Spec: forgejoschrankedev1alpha1.ForgejoSpec{
				Url: forgejoBaseURL,
				AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
					SecretName:      "irrelevant",
					SecretNamespace: "default",
					UsernameKey:     "username",
					PasswordKey:     "password",
				},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, resource)
		}()

		fetched := &forgejoschrankedev1alpha1.Forgejo{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "forgejo-scope-check", Namespace: "some-namespace"}, fetched)
		Expect(errors.IsNotFound(err)).To(BeFalse())
		Expect(fetched.Namespace).To(BeEmpty())
	})
})
