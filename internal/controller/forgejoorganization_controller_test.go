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

var _ = Describe("ForgejoOrganization Controller", func() {
	Context("When reconciling a ForgejoOrganization against a real server", func() {
		const instanceName = "forgejo-org-test-instance"
		const secretName = "forgejo-org-test-creds"
		const resourceName = "acme-corp"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoOrganizationReconciler
			directClient         *forgejoapi.Client
		)

		BeforeEach(func() {
			controllerReconciler = &ForgejoOrganizationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			directClient = forgejoapi.New(forgejoBaseURL, forgejoAdminUsername, forgejoAdminPassword)

			By("creating the credentials secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("creating the Forgejo instance the organization is created on")
			instance := &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: instanceName},
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
			Expect(k8sClient.Create(ctx, instance)).To(Succeed())
		})

		AfterEach(func() {
			By("deleting the ForgejoOrganization and letting the finalizer run")
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: instanceName},
			})
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			})

			// Belt and braces: make sure no organization is left behind on the
			// real server even if a test failed before reaching cleanup.
			_ = directClient.DeleteOrg(ctx, resourceName)
		})

		It("creates a real organization on the Forgejo instance and sets a finalizer", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef:           forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:            "private",
					AdminPermissionRights: true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was updated with a finalizer and Ready status")
			updated := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, organizationFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the organization really exists on the Forgejo server")
			org, err := directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.Name).To(Equal(resourceName))
			Expect(org.Visibility).To(Equal("private"))
			Expect(org.RepoAdminChangeTeamAccess).To(BeTrue())
		})

		It("is idempotent when reconciled again after the organization already exists", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			org, err := directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.Name).To(Equal(resourceName))
		})

		It("updates repo-admin-change-team-access on an existing organization", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef:           forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:            "public",
					AdminPermissionRights: true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			org, err := directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.RepoAdminChangeTeamAccess).To(BeTrue())

			By("flipping AdminPermissionRights to false and reconciling again")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.AdminPermissionRights = false
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			org, err = directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.RepoAdminChangeTeamAccess).To(BeFalse())
		})

		It("adopts an organization that already exists on the server before the CR is created", func() {
			By("creating the organization directly on the server, out of band from the operator")
			_, err := directClient.CreateOrg(ctx, forgejoapi.CreateOrgOptions{
				Name:       resourceName,
				Visibility: "public",
			})
			Expect(err).NotTo(HaveOccurred())

			By("creating a ForgejoOrganization CR for the pre-existing organization and reconciling")
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef:           forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:            "public",
					AdminPermissionRights: true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was adopted: finalizer set and Ready status, not an error from a failed create")
			updated := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, organizationFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the spec was actually applied to the pre-existing organization")
			org, err := directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.Name).To(Equal(resourceName))
			Expect(org.RepoAdminChangeTeamAccess).To(BeTrue())
		})

		It("adopts pre-existing teams on an already-existing organization, applying the requested permissions", func() {
			By("creating the organization and a team directly on the server, out of band from the operator")
			_, err := directClient.CreateOrg(ctx, forgejoapi.CreateOrgOptions{
				Name:       resourceName,
				Visibility: "public",
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = directClient.CreateTeam(ctx, resourceName, forgejoapi.CreateTeamOptions{
				Name:       "developer",
				Permission: "read",
				UnitsMap:   map[string]string{forgejoapi.UnitCode: "read"},
			})
			Expect(err).NotTo(HaveOccurred())

			By("creating a ForgejoOrganization CR that also declares that team with different permissions")
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
					Teams: []forgejoschrankedev1alpha1.Team{
						{
							Name:   "developer",
							Groups: []string{"projects-dev"},
							RepoPermissions: forgejoschrankedev1alpha1.RepoPermissions{
								Code:   "write",
								Issues: "write",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Ready"))

			By("checking the pre-existing team was edited (not duplicated) with the requested permissions")
			teams, err := directClient.ListTeams(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			var developerTeams []forgejoapi.Team
			for _, t := range teams {
				if t.Name == "developer" {
					developerTeams = append(developerTeams, t)
				}
			}
			Expect(developerTeams).To(HaveLen(1))
			Expect(developerTeams[0].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitCode, "write"))
			Expect(developerTeams[0].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitIssues, "write"))
		})

		It("reports an Error status when the referenced Forgejo instance does not exist", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: "does-not-exist"},
					Visibility:  "public",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
			// The finalizer is still added even though the instance couldn't be
			// resolved, so cleanup below can safely delete the resource.
			Expect(controllerutil.ContainsFinalizer(updated, organizationFinalizer)).To(BeTrue())
		})

		It("deletes the real organization from Forgejo when the CR is deleted", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = directClient.GetOrg(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling again to let the finalizer run and remove the organization")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR is fully gone")
			err = k8sClient.Get(ctx, typeNamespacedName, &forgejoschrankedev1alpha1.ForgejoOrganization{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())

			By("checking the organization no longer exists on the real server")
			_, err = directClient.GetOrg(ctx, resourceName)
			Expect(errors.Is(err, forgejoapi.ErrNotFound)).To(BeTrue())
		})

		It("creates, updates, and removes real teams with the requested permissions", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "public",
					Teams: []forgejoschrankedev1alpha1.Team{
						{
							Name:        "developer",
							Description: "day-to-day contributors",
							Groups:      []string{"projects-dev"},
							RepoPermissions: forgejoschrankedev1alpha1.RepoPermissions{
								Code:   "write",
								Issues: "read",
							},
						},
						{
							Name:             "operators",
							Groups:           []string{"its-circle"},
							AccessMode:       "admin",
							CanCreateOrgRepo: true,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking both teams were created with the right permissions on the real server")
			teams, err := directClient.ListTeams(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			byName := map[string]forgejoapi.Team{}
			for _, t := range teams {
				byName[t.Name] = t
			}
			Expect(byName).To(HaveKey("developer"))
			Expect(byName["developer"].Description).To(Equal("day-to-day contributors"))
			Expect(byName["developer"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitCode, "write"))
			Expect(byName["developer"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitIssues, "read"))
			Expect(byName["developer"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitPullRequests, "none"))
			Expect(byName).To(HaveKey("operators"))
			Expect(byName["operators"].Permission).To(Equal("admin"))
			Expect(byName["operators"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitCode, "admin"))
			Expect(byName["operators"].CanCreateOrgRepo).To(BeTrue())
			By("checking the built-in Owners team was left alone")
			Expect(byName).To(HaveKey("Owners"))

			By("switching operators out of admin mode and removing developer, then reconciling again")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Teams = []forgejoschrankedev1alpha1.Team{
				{
					Name:   "operators",
					Groups: []string{"its-circle"},
					RepoPermissions: forgejoschrankedev1alpha1.RepoPermissions{
						Code: "read",
					},
				},
			}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			teams, err = directClient.ListTeams(ctx, resourceName)
			Expect(err).NotTo(HaveOccurred())
			byName = map[string]forgejoapi.Team{}
			for _, t := range teams {
				byName[t.Name] = t
			}
			Expect(byName).NotTo(HaveKey("developer"))
			Expect(byName).To(HaveKey("operators"))
			// Forgejo silently ignores units_map for a team still in admin
			// mode unless the request also carries a non-admin permission;
			// this exercises that a team can actually leave admin mode.
			Expect(byName["operators"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitCode, "read"))
			Expect(byName["operators"].UnitsMap).To(HaveKeyWithValue(forgejoapi.UnitIssues, "none"))
			Expect(byName).To(HaveKey("Owners"))
		})
	})
})
