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
	"encoding/json"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

var _ = Describe("ForgejoRunner Controller", func() {
	Context("When reconciling a ForgejoRunner against a real server", func() {
		const instanceName = "forgejo-runner-test-instance"
		const authSecretName = "forgejo-runner-test-creds"
		const orgName = "runner-org"
		const resourceName = "ci-runner"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Namespace: "default", Name: resourceName}

		var (
			controllerReconciler *ForgejoRunnerReconciler
			directClient         *forgejoapi.Client
		)

		BeforeEach(func() {
			controllerReconciler = &ForgejoRunnerReconciler{
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

			By("creating and reconciling the organization the runner belongs to")
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
			By("deleting the ForgejoRunner and letting the finalizer run")
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{}
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

			// envtest doesn't run the garbage collector, so the runner
			// Secret/Deployment (owned via ownerReferences, not a finalizer)
			// would otherwise survive into the next spec and be mistaken for
			// an already-registered runner.
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			})
			_ = k8sClient.Delete(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			})
			_ = k8sClient.Delete(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-config", Namespace: "default"},
			})
		})

		It("registers a real runner, stores its credentials in a Secret, and deploys it", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName},
					Labels:          []string{"docker:docker://node:22-bookworm"},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR was updated with a finalizer, Ready status, and a runner ID")
			updated := &forgejoschrankedev1alpha1.ForgejoRunner{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(updated, runnerFinalizer)).To(BeTrue())
			Expect(updated.Status.Status).To(Equal("Ready"))
			Expect(updated.Status.RunnerID).NotTo(BeZero())

			By("checking the runner really exists on the Forgejo server")
			runner, err := directClient.GetOrgRunner(ctx, orgName, updated.Status.RunnerID)
			Expect(err).NotTo(HaveOccurred())
			Expect(runner.Name).To(Equal(resourceName))

			By("checking the Secret holds a usable .runner registration file")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, secret)).To(Succeed())
			var regFile runnerRegistrationFile
			Expect(json.Unmarshal(secret.Data[runnerSecretKey], &regFile)).To(Succeed())
			Expect(regFile.ID).To(Equal(updated.Status.RunnerID))
			Expect(regFile.Name).To(Equal(resourceName))
			Expect(regFile.Address).To(Equal(forgejoBaseURL))
			Expect(regFile.Labels).To(Equal([]string{"docker:docker://node:22-bookworm"}))
			Expect(regFile.Token).NotTo(BeEmpty())

			By("checking a Deployment for the runner and its dind sidecar was created")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deployment)).To(Succeed())
			Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2))
			runnerContainer := findContainer(deployment, "runner")
			Expect(runnerContainer.Image).To(Equal(defaultRunnerImage))
			dindContainer := findContainer(deployment, dindContainerName)
			Expect(dindContainer.Image).To(Equal(defaultDindImage))
			Expect(*dindContainer.SecurityContext.Privileged).To(BeTrue())
		})

		It("does not re-register or overwrite the Secret when reconciled again", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			secretBefore := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, secretBefore)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			secretAfter := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, secretAfter)).To(Succeed())
			Expect(secretAfter.Data[runnerSecretKey]).To(Equal(secretBefore.Data[runnerSecretKey]))
		})

		It("uses the default image and labels when unset", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, secret)).To(Succeed())
			var regFile runnerRegistrationFile
			Expect(json.Unmarshal(secret.Data[runnerSecretKey], &regFile)).To(Succeed())
			Expect(regFile.Labels).To(Equal(defaultRunnerLabels))

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deployment)).To(Succeed())
			Expect(findContainer(deployment, "runner").Image).To(Equal(defaultRunnerImage))
			Expect(findContainer(deployment, dindContainerName).Image).To(Equal(defaultDindImage))
		})

		It("generates a runner config.yaml and mounts it only when dockerAccess is set", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName},
					DockerAccess:    true,
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking a ConfigMap holding config.yaml was created and mounted into the runner container")
			configMap := &corev1.ConfigMap{}
			configMapName := types.NamespacedName{Namespace: "default", Name: resourceName + "-config"}
			Expect(k8sClient.Get(ctx, configMapName, configMap)).To(Succeed())
			Expect(configMap.Data[runnerConfigFileName]).To(ContainSubstring("DOCKER_HOST"))
			Expect(configMap.Data[runnerConfigFileName]).To(ContainSubstring("network: host"))

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deployment)).To(Succeed())
			runnerContainer := findContainer(deployment, "runner")
			Expect(runnerContainer.Command[2]).To(ContainSubstring("--config " + runnerConfigDir + "/" + runnerConfigFileName))
			found := false
			for _, vm := range runnerContainer.VolumeMounts {
				if vm.Name == runnerConfigVolume {
					found = true
				}
			}
			Expect(found).To(BeTrue())

			By("checking the ConfigMap is removed again once dockerAccess is unset")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.DockerAccess = false
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Get(ctx, configMapName, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("reports an Error status when the referenced ForgejoOrganization does not exist", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: "does-not-exist"},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoRunner{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Error"))
			Expect(updated.Status.Message).NotTo(BeEmpty())
			Expect(controllerutil.ContainsFinalizer(updated, runnerFinalizer)).To(BeTrue())
		})

		It("resolves the organization across namespaces when organizationRef.namespace is set", func() {
			const runnerNamespace = "ci-runners"
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runnerNamespace}})

			crossNamespacedName := types.NamespacedName{Namespace: runnerNamespace, Name: resourceName}
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: runnerNamespace},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName, Namespace: "default"},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: crossNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR itself, and its owned Secret/Deployment, all live in the runner's own namespace")
			updated := &forgejoschrankedev1alpha1.ForgejoRunner{}
			Expect(k8sClient.Get(ctx, crossNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Status).To(Equal("Ready"))
			Expect(updated.Status.RunnerID).NotTo(BeZero())

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, crossNamespacedName, secret)).To(Succeed())
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, crossNamespacedName, deployment)).To(Succeed())

			By("checking the runner really exists on the Forgejo server, registered under the cross-namespace organization")
			runner, err := directClient.GetOrgRunner(ctx, orgName, updated.Status.RunnerID)
			Expect(err).NotTo(HaveOccurred())
			Expect(runner.Name).To(Equal(resourceName))

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: crossNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = directClient.GetOrgRunner(ctx, orgName, updated.Status.RunnerID)
			Expect(errors.Is(err, forgejoapi.ErrNotFound)).To(BeTrue())
		})

		It("deregisters the real runner from Forgejo when the CR is deleted", func() {
			resource := &forgejoschrankedev1alpha1.ForgejoRunner{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: forgejoschrankedev1alpha1.ForgejoRunnerSpec{
					OrganizationRef: forgejoschrankedev1alpha1.RunnerOrganizationRef{Name: orgName},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &forgejoschrankedev1alpha1.ForgejoRunner{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			runnerID := updated.Status.RunnerID

			_, err = directClient.GetOrgRunner(ctx, orgName, runnerID)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CR, which only sets a deletion timestamp because of the finalizer")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("reconciling again to let the finalizer run and deregister the runner")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking the CR is fully gone")
			err = k8sClient.Get(ctx, typeNamespacedName, &forgejoschrankedev1alpha1.ForgejoRunner{})
			Expect(client.IgnoreNotFound(err)).To(Succeed())

			By("checking the runner no longer exists on the real server")
			_, err = directClient.GetOrgRunner(ctx, orgName, runnerID)
			Expect(errors.Is(err, forgejoapi.ErrNotFound)).To(BeTrue())
		})
	})
})

// findContainer returns the container named name in deployment's pod
// template, failing the spec if it isn't found.
func findContainer(deployment *appsv1.Deployment, name string) *corev1.Container {
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == name {
			return &deployment.Spec.Template.Spec.Containers[i]
		}
	}
	Fail(fmt.Sprintf("no container named %q in deployment %s/%s", name, deployment.Namespace, deployment.Name))
	return nil
}
