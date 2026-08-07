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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

// createTestReceiver creates a real (unstructured) Flux Receiver object
// against the envtest apiserver, with the given webhook path and a
// spec.secretRef pointing at a Secret containing the given token, both in
// the same namespace as the Receiver (mirroring Flux's own convention).
func createTestReceiver(ctx context.Context, name, namespace, webhookPath, token string) {
	secretName := name + "-token"
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		StringData: map[string]string{fluxReceiverSecretTokenKey: token},
	})).To(Succeed())

	receiver := &unstructured.Unstructured{}
	receiver.SetGroupVersionKind(fluxReceiverGVK)
	receiver.SetName(name)
	receiver.SetNamespace(namespace)
	Expect(unstructured.SetNestedField(receiver.Object, "generic", "spec", "type")).To(Succeed())
	Expect(unstructured.SetNestedField(receiver.Object, secretName, "spec", "secretRef", "name")).To(Succeed())
	Expect(k8sClient.Create(ctx, receiver)).To(Succeed())

	Expect(unstructured.SetNestedField(receiver.Object, webhookPath, "status", "webhookPath")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, receiver)).To(Succeed())
}

func deleteTestReceiver(ctx context.Context, name, namespace string) {
	receiver := &unstructured.Unstructured{}
	receiver.SetGroupVersionKind(fluxReceiverGVK)
	_ = k8sClient.Delete(ctx, &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": receiver.GetAPIVersion(),
			"kind":       receiver.GetKind(),
			"metadata":   map[string]any{"name": name, "namespace": namespace},
		},
	})
	_ = k8sClient.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: namespace},
	})
}

var _ = Describe("ForgejoWebhook Controller", func() {
	Context("When reconciling a ForgejoWebhook against a real server", func() {
		const instanceName = "forgejo-webhook-test-instance"
		const authSecretName = "forgejo-webhook-test-creds"
		const orgName = "webhook-org"
		const repoName = "webapp"
		const receiverName = "test-receiver"
		const receiverNamespace = "flux-system"
		const webhookPath = "/hook/abc123"
		const receiverToken = "s3cr3t-token"
		const instanceBase = "https://flux-webhook.example.com"
		const resourceName = "webapp-webhook"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoWebhookReconciler
			directClient         *forgejoapi.Client
		)

		findHook := func() *forgejoapi.Hook {
			hooks, err := directClient.ListRepoHooks(ctx, orgName, repoName)
			Expect(err).NotTo(HaveOccurred())
			for i := range hooks {
				if hooks[i].Config["url"] == instanceBase+webhookPath {
					return &hooks[i]
				}
			}
			return nil
		}

		BeforeEach(func() {
			controllerReconciler = &ForgejoWebhookReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			directClient = forgejoapi.New(forgejoBaseURL, forgejoAdminUsername, forgejoAdminPassword)

			By("creating the flux-system namespace and a Receiver in it")
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: receiverNamespace}})
			createTestReceiver(ctx, receiverName, receiverNamespace, webhookPath, receiverToken)

			By("creating the Forgejo instance with a base configured")
			authSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: authSecretName, Namespace: "default"},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

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
					Base: instanceBase,
				},
			}
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())

			By("creating and reconciling the organization and repository the webhook targets")
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

			repo := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: repoName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, repo)).To(Succeed())
			repoReconciler := &ForgejoRepositoryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = repoReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: repoName}})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			repoReconciler := &ForgejoRepositoryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			repo := &forgejoschrankedev1alpha1.ForgejoRepository{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: repoName}, repo); err == nil {
				Expect(k8sClient.Delete(ctx, repo)).To(Succeed())
				_, err := repoReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: repoName}})
				Expect(err).NotTo(HaveOccurred())
			}

			orgReconciler := &ForgejoOrganizationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			org := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: orgName}, org); err == nil {
				Expect(k8sClient.Delete(ctx, org)).To(Succeed())
				_, err := orgReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: orgName}})
				Expect(err).NotTo(HaveOccurred())
			}

			_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{ObjectMeta: metav1.ObjectMeta{Name: instanceName}})
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: authSecretName, Namespace: "default"}})
			deleteTestReceiver(ctx, receiverName, receiverNamespace)

			// Belt and braces: make sure no repo/hook is left behind on the
			// real server even if a test failed before reaching cleanup.
			_ = directClient.DeleteRepo(ctx, orgName, repoName)
		})

		It("creates a real webhook on the repository, sourced from a Receiver in another namespace", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					RepositoryRef: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryRef{Name: repoName},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was updated with a finalizer and Ready status")
			updated := &forgejoschrankedev1alpha1.ForgejoWebhook{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, webhookFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))
			Expect(updated.Status.URL).To(Equal(instanceBase + webhookPath))
			Expect(updated.Status.HookID).NotTo(BeZero())

			By("checking the webhook really exists on the Forgejo server")
			hook := findHook()
			Expect(hook).NotTo(BeNil())
			Expect(hook.Type).To(Equal(defaultHookType))
			Expect(hook.Events).To(ConsistOf(defaultHookEvent))
			Expect(hook.Active).To(BeTrue())
			Expect(hook.Config["content_type"]).To(Equal("json"))
		})

		It("is idempotent when reconciled again after the webhook already exists", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					RepositoryRef: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryRef{Name: repoName},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			hooks, err := directClient.ListRepoHooks(ctx, orgName, repoName)
			Expect(err).NotTo(HaveOccurred())
			matching := 0
			for _, h := range hooks {
				if h.Config["url"] == instanceBase+webhookPath {
					matching++
				}
			}
			Expect(matching).To(Equal(1))
		})

		It("suspends the webhook on Forgejo when spec.suspend is set", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					RepositoryRef: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryRef{Name: repoName},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(findHook().Active).To(BeTrue())

			By("suspending and reconciling again")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Suspend = true
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(findHook().Active).To(BeFalse())
		})

		It("targets a repository directly by URL, without a ForgejoRepository object", func() {
			const directRepoName = "no-cr-repo"
			_, err := directClient.CreateOrgRepo(ctx, orgName, forgejoapi.CreateRepoOptions{Name: directRepoName})
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = directClient.DeleteRepo(ctx, orgName, directRepoName) }()

			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					Repository: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryTarget{
						InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
						URL:         fmt.Sprintf("%s/%s/%s", forgejoBaseURL, orgName, directRepoName),
					},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			hooks, err := directClient.ListRepoHooks(ctx, orgName, directRepoName)
			Expect(err).NotTo(HaveOccurred())
			Expect(hooks).To(HaveLen(1))
			Expect(hooks[0].Config["url"]).To(Equal(instanceBase + webhookPath))
		})

		It("reports an Error status when the Forgejo instance has no spec.base configured", func() {
			noBaseInstanceName := instanceName + "-no-base"
			Expect(k8sClient.Create(ctx, &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: noBaseInstanceName},
				Spec: forgejoschrankedev1alpha1.ForgejoSpec{
					Url: forgejoBaseURL,
					AuthSecretRef: forgejoschrankedev1alpha1.AuthSecretRef{
						SecretName:      authSecretName,
						SecretNamespace: "default",
						UsernameKey:     "username",
						PasswordKey:     "password",
					},
				},
			})).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{ObjectMeta: metav1.ObjectMeta{Name: noBaseInstanceName}})
			}()

			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					Repository: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryTarget{
						InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: noBaseInstanceName},
						URL:         fmt.Sprintf("%s/%s/%s", forgejoBaseURL, orgName, repoName),
					},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoWebhook{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).To(ContainSubstring("spec.base"))
		})

		It("deletes the real webhook from Forgejo when the CR is deleted", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					RepositoryRef: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryRef{Name: repoName},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(findHook()).NotTo(BeNil())

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling again to let the finalizer run and remove the webhook")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR is fully gone")
			err = k8sClient.Get(ctx, typeNamespacedName, &forgejoschrankedev1alpha1.ForgejoWebhook{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())

			By("checking the webhook no longer exists on the real server")
			Expect(findHook()).To(BeNil())
		})

		It("rejects a ForgejoWebhook that sets both repositoryRef and repository", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					RepositoryRef: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryRef{Name: repoName},
					Repository: &forgejoschrankedev1alpha1.ForgejoWebhookRepositoryTarget{
						InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
						URL:         fmt.Sprintf("%s/%s/%s", forgejoBaseURL, orgName, repoName),
					},
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			err := k8sClient.Create(ctx, resource)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one of repositoryRef or repository"))
		})

		It("rejects a ForgejoWebhook that sets neither repositoryRef nor repository", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoWebhook{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoWebhookSpec{
					ReceiverRef: forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef{
						Name:      receiverName,
						Namespace: receiverNamespace,
					},
				},
			}
			err := k8sClient.Create(ctx, resource)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one of repositoryRef or repository"))
		})
	})
})
