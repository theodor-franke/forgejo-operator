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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ForgejoWebhookRepositoryRef references a ForgejoRepository object, in the
// same namespace as the ForgejoWebhook doing the referencing.
type ForgejoWebhookRepositoryRef struct {
	Name string `json:"name"`
}

// ForgejoWebhookRepositoryTarget targets a repository directly, without
// requiring a ForgejoRepository object to exist for it (e.g. a repository
// this operator doesn't otherwise manage).
type ForgejoWebhookRepositoryTarget struct {
	// InstanceRef references the cluster-scoped Forgejo instance the
	// repository lives on.
	InstanceRef InstanceRef `json:"instanceRef"`

	// URL is the repository's web URL on that instance, e.g.
	// https://forgejo.example.com/my-org/my-repo. The owner and repository
	// name are parsed out of its path; the scheme and host are otherwise
	// unused (the instance's own spec.url is what's actually called).
	URL string `json:"url"`
}

// ForgejoWebhookReceiverRef references a Flux
// notification.toolkit.fluxcd.io/v1 Receiver object, which supplies this
// webhook's target URL (spec.status.webhookPath) and shared secret
// (spec.secretRef). Namespace is always required since a Receiver commonly
// lives in a different namespace than the ForgejoWebhook referencing it
// (e.g. a shared flux-system namespace).
type ForgejoWebhookReceiverRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ForgejoWebhookSpec defines the desired state of ForgejoWebhook.
//
// +kubebuilder:validation:XValidation:rule="has(self.repositoryRef) != has(self.repository)",message="exactly one of repositoryRef or repository must be set"
type ForgejoWebhookSpec struct {
	// RepositoryRef references a ForgejoRepository object, in the same
	// namespace as this object, to create the webhook on. Exactly one of
	// RepositoryRef or Repository must be set.
	// +optional
	RepositoryRef *ForgejoWebhookRepositoryRef `json:"repositoryRef,omitempty"`

	// Repository targets a repository directly by URL, without going
	// through a ForgejoRepository object. Exactly one of RepositoryRef or
	// Repository must be set.
	// +optional
	Repository *ForgejoWebhookRepositoryTarget `json:"repository,omitempty"`

	// ReceiverRef references the Flux Receiver object this webhook
	// delivers events to. Its status.webhookPath is combined with the
	// target Forgejo instance's spec.base to build the webhook's target
	// URL, and the Secret it references (spec.secretRef) supplies the
	// webhook's shared signing secret.
	ReceiverRef ForgejoWebhookReceiverRef `json:"receiverRef"`

	// Type is the Forgejo webhook payload type. Defaults to "forgejo",
	// which is what Flux's "gitea" Receiver type expects (Forgejo is a
	// Gitea fork and shares its webhook signature format).
	// +optional
	Type string `json:"type,omitempty"`

	// Events lists the repository events that trigger this webhook.
	// Defaults to ["push"].
	// +optional
	Events []string `json:"events,omitempty"`

	// BranchFilter restricts push/create/delete events to matching
	// branches (glob syntax). Defaults to "*" (all branches).
	// +optional
	BranchFilter string `json:"branchFilter,omitempty"`

	// Suspend disables the webhook on Forgejo without deleting it.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ForgejoWebhookStatus defines the observed state of ForgejoWebhook.
type ForgejoWebhookStatus struct {
	// Status is a short, human-readable summary of the reconciliation state,
	// e.g. "Ready" or "Error".
	// +optional
	Status string `json:"status,omitempty"`

	// Message contains additional detail when Status is not "Ready", such as
	// the last error encountered while talking to the Forgejo instance or
	// resolving the referenced Receiver.
	// +optional
	Message string `json:"message,omitempty"`

	// URL is the full webhook target URL last registered on Forgejo,
	// computed from the referenced Receiver's status.webhookPath and the
	// Forgejo instance's spec.base.
	// +optional
	URL string `json:"url,omitempty"`

	// HookID is the ID of the webhook object on the Forgejo repository.
	// +optional
	HookID int64 `json:"hookID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ForgejoWebhook is the Schema for the forgejowebhooks API.
type ForgejoWebhook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoWebhookSpec   `json:"spec,omitempty"`
	Status ForgejoWebhookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoWebhookList contains a list of ForgejoWebhook.
type ForgejoWebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgejoWebhook `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ForgejoWebhook{}, &ForgejoWebhookList{})
}
