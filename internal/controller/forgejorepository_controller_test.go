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
	"errors"
	"strings"

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

var _ = Describe("ForgejoRepository Controller", func() {
	Context("When reconciling a ForgejoRepository against a real server", func() {
		const instanceName = "forgejo-repository-test-instance"
		const authSecretName = "forgejo-repository-test-creds"
		const orgName = "repository-org"
		const resourceName = "webapp"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoRepositoryReconciler
			directClient         *forgejoapi.Client
		)

		BeforeEach(func() {
			controllerReconciler = &ForgejoRepositoryReconciler{
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

			By("creating and reconciling the organization the repository belongs to")
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
		})

		AfterEach(func() {
			By("deleting the ForgejoRepository and letting the finalizer run")
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

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

			// Belt and braces: make sure no repository is left behind on the
			// real server even if a test failed before reaching cleanup.
			_ = directClient.DeleteRepo(ctx, orgName, resourceName)
		})

		It("creates a real repository on the organization and sets a finalizer", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Description:     "a test repository",
					Private:         true,
					AutoInit:        true,
					DefaultBranch:   "main",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was updated with a finalizer and Ready status")
			updated := &forgejoschrankedev1alpha1.ForgejoRepository{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, repositoryFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the repository really exists on the Forgejo server")
			repo, err := directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Name).To(Equal(resourceName))
			Expect(repo.Description).To(Equal("a test repository"))
			Expect(repo.Private).To(BeTrue())
			Expect(repo.DefaultBranch).To(Equal("main"))
		})

		It("is idempotent when reconciled again after the repository already exists", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			repo, err := directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Name).To(Equal(resourceName))
		})

		It("updates description, visibility, and template flag on an existing repository", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Description:     "initial description",
					Private:         false,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			repo, err := directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Description).To(Equal("initial description"))
			Expect(repo.Private).To(BeFalse())
			Expect(repo.Template).To(BeFalse())

			By("changing the spec and reconciling again")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Description = "updated description"
			resource.Spec.Private = true
			resource.Spec.Template = true
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			repo, err = directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Description).To(Equal("updated description"))
			Expect(repo.Private).To(BeTrue())
			Expect(repo.Template).To(BeTrue())
		})

		It("adopts a repository that already exists on the server before the CR is created", func() {
			By("creating the repository directly on the server, out of band from the operator")
			_, err := directClient.CreateOrgRepo(ctx, orgName, forgejoapi.CreateRepoOptions{
				Name:        resourceName,
				Description: "created outside the operator",
				Private:     false,
			})
			Expect(err).NotTo(HaveOccurred())

			By("creating a ForgejoRepository CR for the pre-existing repository and reconciling")
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					Description:     "adopted by the operator",
					Private:         true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was adopted: finalizer set and Ready status, not an error from a failed create")
			updated := &forgejoschrankedev1alpha1.ForgejoRepository{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, repositoryFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the spec was actually applied to the pre-existing repository")
			repo, err := directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Name).To(Equal(resourceName))
			Expect(repo.Description).To(Equal("adopted by the operator"))
			Expect(repo.Private).To(BeTrue())
		})

		It("reports an Error status when the referenced ForgejoOrganization does not exist", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: "does-not-exist"},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoRepository{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
			// The finalizer is still added even though the organization couldn't
			// be resolved, so cleanup above can safely delete the resource.
			Expect(controllerutil.ContainsFinalizer(updated, repositoryFinalizer)).To(BeTrue())
		})

		It("deletes the real repository from Forgejo when the CR is deleted", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = directClient.GetRepo(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling again to let the finalizer run and remove the repository")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR is fully gone")
			err = k8sClient.Get(ctx, typeNamespacedName, &forgejoschrankedev1alpha1.ForgejoRepository{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())

			By("checking the repository no longer exists on the real server")
			_, err = directClient.GetRepo(ctx, orgName, resourceName)
			Expect(errors.Is(err, forgejoapi.ErrNotFound)).To(BeTrue())
		})
	})

	Context("When the Forgejo instance has a default pull key configured", func() {
		const instanceName = "forgejo-repository-pullkey-test-instance"
		const authSecretName = "forgejo-repository-pullkey-test-creds"
		const pullKeySecretName = "forgejo-repository-pullkey-test-pullkey"
		const orgName = "repository-pullkey-org"
		const resourceName = "webapp"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoRepositoryReconciler
			directClient         *forgejoapi.Client
		)

		hasDefaultPullKey := func() bool {
			keys, err := directClient.ListDeployKeys(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			for _, k := range keys {
				if k.Title == defaultPullKeyTitle {
					return true
				}
			}
			return false
		}

		BeforeEach(func() {
			controllerReconciler = &ForgejoRepositoryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			directClient = forgejoapi.New(forgejoBaseURL, forgejoAdminUsername, forgejoAdminPassword)

			authSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: authSecretName, Namespace: "default"},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, authSecret)).To(Succeed())

			By("creating the Forgejo instance with a default pull key configured")
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
					DefaultPullKeySecretRef: &forgejoschrankedev1alpha1.PullKeySecretRef{
						SecretName:      pullKeySecretName,
						SecretNamespace: "default",
					},
					SSHHost: forgejoSSHHost,
					SSHPort: forgejoSSHPort,
				},
			}
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())
			instanceReconciler := &ForgejoReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := instanceReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: instanceName}})
			Expect(err).NotTo(HaveOccurred())

			org := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: orgName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
				},
			}
			Expect(k8sClient.Create(ctx, org)).To(Succeed())
			orgReconciler := &ForgejoOrganizationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = orgReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: orgName}})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
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
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pullKeySecretName, Namespace: "default"}})
			_ = directClient.DeleteRepo(ctx, orgName, resourceName)
		})

		It("adds the default pull key as a read-only deploy key by default", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(hasDefaultPullKey()).To(BeTrue())

			By("checking it matches the public key stored in the pull key secret and is read-only")
			pullKeySecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pullKeySecretName, Namespace: "default"}, pullKeySecret)).To(Succeed())

			keys, err := directClient.ListDeployKeys(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(HaveLen(1))
			Expect(keys[0].ReadOnly).To(BeTrue())
			Expect(keys[0].Key).To(Equal(strings.TrimSpace(string(pullKeySecret.Data["publicKey"]))))

			By("reconciling again and checking the key isn't duplicated")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			keys, err = directClient.ListDeployKeys(ctx, orgName, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(HaveLen(1))
		})

		It("does not add the default pull key when the repository opts out", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef:      forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
					OptOutDefaultPullKey: true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(hasDefaultPullKey()).To(BeFalse())
		})

		It("removes the default pull key when a repository opts out after already having it", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRepository{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRepositorySpec{
					OrganizationRef: forgejoschrankedev1alpha1.OrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(hasDefaultPullKey()).To(BeTrue())

			By("opting out and reconciling again")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.OptOutDefaultPullKey = true
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(hasDefaultPullKey()).To(BeFalse())
		})
	})
})
