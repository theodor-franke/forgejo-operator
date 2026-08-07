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

package v1alpha1

import (
	"fmt"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

// These specs go through the real admission chain: they Create/Update
// objects via k8sClient against the envtest API server, which the running
// webhook server (see webhook_suite_test.go) actually intercepts over TLS —
// they do not call the validator's methods directly.
//
// envtest doesn't run the namespace controller, so a deleted Namespace
// stays stuck "Terminating" forever instead of actually going away. Rather
// than deleting and reusing a couple of fixed namespaces (which would break
// on the second test), each test gets its own freshly created, never
// deleted namespaces; that's cheap and the whole envtest instance is torn
// down at the end of the suite anyway.
var testCounter int64

// nextTestID returns a counter unique to this test run, so tests never
// collide over shared namespace or instance names either. It's not just
// namespaces that outlive a test here: nothing deletes the
// ForgejoOrganizations created below either, precisely because the webhook
// under test itself makes that collide across tests unless every input is
// unique.
func nextTestID() int64 {
	return atomic.AddInt64(&testCounter, 1)
}

func newTestNamespace() string {
	name := fmt.Sprintf("collision-test-%d", nextTestID())
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func newTestInstanceName() string {
	return fmt.Sprintf("instance-%d", nextTestID())
}

var _ = Describe("ForgejoOrganization Webhook", func() {
	newOrg := func(namespace, name, instance string) *forgejoschrankedev1alpha1.ForgejoOrganization {
		return &forgejoschrankedev1alpha1.ForgejoOrganization{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: forgejoschrankedev1alpha1.ForgejoOrganizationSpec{
				InstanceRef: forgejoschrankedev1alpha1.InstanceRef{Name: instance},
			},
		}
	}

	Context("When creating a ForgejoOrganization", func() {
		It("admits it when no other organization shares its name", func() {
			ns := newTestNamespace()
			Expect(k8sClient.Create(ctx, newOrg(ns, "acme", newTestInstanceName()))).To(Succeed())
		})

		It("admits two organizations with the same name targeting different instances", func() {
			nsA, nsB := newTestNamespace(), newTestNamespace()
			Expect(k8sClient.Create(ctx, newOrg(nsA, "acme", newTestInstanceName()))).To(Succeed())
			Expect(k8sClient.Create(ctx, newOrg(nsB, "acme", newTestInstanceName()))).To(Succeed())
		})

		It("rejects an organization whose name collides with one in another namespace targeting the same instance", func() {
			nsA, nsB := newTestNamespace(), newTestNamespace()
			instance := newTestInstanceName()
			Expect(k8sClient.Create(ctx, newOrg(nsA, "acme", instance))).To(Succeed())

			err := k8sClient.Create(ctx, newOrg(nsB, "acme", instance))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists in namespace"))
			Expect(err.Error()).To(ContainSubstring(instance))
		})

		It("admits a same-named organization in the same namespace being rejected by Kubernetes itself, not the webhook", func() {
			// Kubernetes itself rejects a second object with the same
			// namespace+name outright; this documents that the webhook
			// isn't what's doing the rejecting in that case.
			ns := newTestNamespace()
			instance := newTestInstanceName()
			Expect(k8sClient.Create(ctx, newOrg(ns, "acme", instance))).To(Succeed())
			err := k8sClient.Create(ctx, newOrg(ns, "acme", instance))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).NotTo(ContainSubstring("organization names must be unique"))
		})
	})

	Context("When updating a ForgejoOrganization", func() {
		It("admits changes that don't introduce a collision", func() {
			ns := newTestNamespace()
			org := newOrg(ns, "acme", newTestInstanceName())
			Expect(k8sClient.Create(ctx, org)).To(Succeed())

			org.Spec.Visibility = "private"
			Expect(k8sClient.Update(ctx, org)).To(Succeed())
		})

		It("rejects re-pointing instanceRef into a collision with an existing organization elsewhere", func() {
			nsA, nsB := newTestNamespace(), newTestNamespace()
			instanceOne, instanceTwo := newTestInstanceName(), newTestInstanceName()
			Expect(k8sClient.Create(ctx, newOrg(nsA, "acme", instanceOne))).To(Succeed())
			orgB := newOrg(nsB, "acme", instanceTwo)
			Expect(k8sClient.Create(ctx, orgB)).To(Succeed())

			orgB.Spec.InstanceRef.Name = instanceOne
			err := k8sClient.Update(ctx, orgB)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists in namespace"))
		})
	})
})
