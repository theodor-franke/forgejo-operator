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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var forgejoorganizationlog = logf.Log.WithName("forgejoorganization-resource")

// SetupForgejoOrganizationWebhookWithManager registers the webhook for ForgejoOrganization in the manager.
func SetupForgejoOrganizationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&forgejoschrankedev1alpha1.ForgejoOrganization{}).
		WithValidator(&ForgejoOrganizationCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-forgejo-schranke-de-v1alpha1-forgejoorganization,mutating=false,failurePolicy=fail,sideEffects=None,groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=create;update,versions=v1alpha1,name=vforgejoorganization-v1alpha1.kb.io,admissionReviewVersions=v1

// ForgejoOrganizationCustomValidator validates that a ForgejoOrganization's
// name does not collide with another ForgejoOrganization, in a different
// namespace, that targets the same Forgejo instance. Both would otherwise
// map to the same remote organization (the remote name is just
// metadata.name), silently fighting over its settings and teams.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ForgejoOrganizationCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &ForgejoOrganizationCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ForgejoOrganization.
func (v *ForgejoOrganizationCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	org, ok := obj.(*forgejoschrankedev1alpha1.ForgejoOrganization)
	if !ok {
		return nil, fmt.Errorf("expected a ForgejoOrganization object but got %T", obj)
	}
	forgejoorganizationlog.Info("Validation for ForgejoOrganization upon creation", "name", org.GetName())

	return nil, v.checkForCollision(ctx, org)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ForgejoOrganization.
func (v *ForgejoOrganizationCustomValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	org, ok := newObj.(*forgejoschrankedev1alpha1.ForgejoOrganization)
	if !ok {
		return nil, fmt.Errorf("expected a ForgejoOrganization object for the newObj but got %T", newObj)
	}
	forgejoorganizationlog.Info("Validation for ForgejoOrganization upon update", "name", org.GetName())

	// instanceRef can change on update too, so re-check even though the
	// name/instance pair was valid when this object was first created.
	return nil, v.checkForCollision(ctx, org)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ForgejoOrganization.
// Deleting an organization can never create a collision, so there is
// nothing to validate here.
func (v *ForgejoOrganizationCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// checkForCollision rejects org if some other ForgejoOrganization, in a
// different namespace, already targets the same Forgejo instance under the
// same name. Two objects can never share both namespace and name, so
// comparing namespaces here also correctly skips org itself on update.
func (v *ForgejoOrganizationCustomValidator) checkForCollision(ctx context.Context, org *forgejoschrankedev1alpha1.ForgejoOrganization) error {
	list := &forgejoschrankedev1alpha1.ForgejoOrganizationList{}
	if err := v.Client.List(ctx, list); err != nil {
		return fmt.Errorf("listing ForgejoOrganizations to check for name collisions: %w", err)
	}

	for _, other := range list.Items {
		if other.Name != org.Name || other.Namespace == org.Namespace {
			continue
		}
		if other.Spec.InstanceRef.Name != org.Spec.InstanceRef.Name {
			continue
		}
		return fmt.Errorf(
			"ForgejoOrganization %q already exists in namespace %q and targets the same Forgejo instance %q; "+
				"organization names must be unique per Forgejo instance across all namespaces",
			org.Name, other.Namespace, org.Spec.InstanceRef.Name,
		)
	}
	return nil
}
