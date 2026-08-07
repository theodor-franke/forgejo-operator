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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

var _ = Describe("Group-to-team map aggregation", func() {
	Context("When multiple ForgejoOrganizations target the same instance", func() {
		const instanceName = "forgejo-groupmap-test-instance"
		const secretName = "forgejo-groupmap-test-creds"

		const orgItsOps = "its-ops"
		const orgCITS = "cits"
		const orgBAnz = "banz"

		ctx := context.Background()

		var controllerReconciler *ForgejoOrganizationReconciler

		reconcileOrg := func(name string) {
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
			Expect(err).NotTo(HaveOccurred())
		}

		deleteOrg := func(name string) {
			resource := &forgejoschrankedev1alpha1.ForgejoOrganization{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, resource); err != nil {
				return
			}
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			reconcileOrg(name)
		}

		BeforeEach(func() {
			controllerReconciler = &ForgejoOrganizationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
				StringData: map[string]string{
					"username": forgejoAdminUsername,
					"password": forgejoAdminPassword,
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

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
			deleteOrg(orgItsOps)
			deleteOrg(orgCITS)
			deleteOrg(orgBAnz)

			_ = k8sClient.Delete(ctx, &forgejoschrankedev1alpha1.Forgejo{
				ObjectMeta: metav1.ObjectMeta{Name: instanceName},
			})
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			})
		})

		It("pushes the combined group->org->team map to the instance's first OAuth2 source", func() {
			By("creating three organizations whose teams share some claimed groups")
			itsOps := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: orgItsOps, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "private",
					Teams: []forgejoschrankedev1alpha1.Team{
						{Name: "developer", Groups: []string{"projects-dev"}},
						{Name: "operators", Groups: []string{"its-circle-plattformautomatisierung", "its-circle-it-sicherheit"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, itsOps)).To(Succeed())

			cits := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: orgCITS, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "private",
					Teams: []forgejoschrankedev1alpha1.Team{
						{Name: "operators", Groups: []string{"its-circle-it-sicherheit"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cits)).To(Succeed())

			banz := &forgejoschrankedev1alpha1.ForgejoOrganization{
				ObjectMeta: metav1.ObjectMeta{Name: orgBAnz, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
					InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instanceName},
					Visibility:  "private",
					Teams: []forgejoschrankedev1alpha1.Team{
						{Name: "developers", Groups: []string{"projects-banz"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, banz)).To(Succeed())

			By("reconciling all three organizations")
			reconcileOrg(orgItsOps)
			reconcileOrg(orgCITS)
			reconcileOrg(orgBAnz)

			By("checking the aggregated map stored on the real OAuth2 authentication source")
			Expect(readStoredGroupTeamMap(ctx)).To(Equal(map[string]map[string][]string{
				"projects-dev": {
					orgItsOps: {"developer"},
				},
				"its-circle-plattformautomatisierung": {
					orgItsOps: {"operators"},
				},
				"its-circle-it-sicherheit": {
					orgItsOps: {"operators"},
					orgCITS:   {"operators"},
				},
				"projects-banz": {
					orgBAnz: {"developers"},
				},
			}))

			By("deleting one organization and checking its groups drop out of the map")
			deleteOrg(orgCITS)

			Expect(readStoredGroupTeamMap(ctx)).To(Equal(map[string]map[string][]string{
				"projects-dev": {
					orgItsOps: {"developer"},
				},
				"its-circle-plattformautomatisierung": {
					orgItsOps: {"operators"},
				},
				"its-circle-it-sicherheit": {
					orgItsOps: {"operators"},
				},
				"projects-banz": {
					orgBAnz: {"developers"},
				},
			}))
		})
	})
})
