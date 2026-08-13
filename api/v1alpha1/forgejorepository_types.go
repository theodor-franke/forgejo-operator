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

// ForgejoRepositorySpec defines the desired state of ForgejoRepository.
type ForgejoRepositorySpec struct {
	// OrganizationRef references the ForgejoOrganization this repository
	// belongs to. Defaults to this object's own namespace when
	// organizationRef.namespace is unset.
	OrganizationRef OrganizationRef `json:"organizationRef"`

	// Description is shown on the repository's page in Forgejo.
	// +optional
	Description string `json:"description,omitempty"`

	// Private makes the repository visible only to the organization's
	// members and explicitly added collaborators.
	// +optional
	Private bool `json:"private,omitempty"`

	// Template marks the repository as a template others can generate new
	// repositories from.
	// +optional
	Template bool `json:"template,omitempty"`

	// AutoInit creates an initial commit (README, .gitignore, LICENSE per
	// the fields below) when the repository is first created. Ignored once
	// the repository already exists. A repository created without an
	// initial commit has no DefaultBranch until something is pushed to it.
	// +optional
	AutoInit bool `json:"autoInit,omitempty"`

	// DefaultBranch sets the repository's default branch. On creation it
	// only takes effect when AutoInit is true.
	// +optional
	DefaultBranch string `json:"defaultBranch,omitempty"`

	// OptOutDefaultPullKey skips adding the Forgejo instance's shared
	// default deploy key (see Forgejo.spec.defaultPullKeySecretRef) to this
	// repository. Has no effect if the instance doesn't have one configured.
	// +optional
	OptOutDefaultPullKey bool `json:"optOutDefaultPullKey,omitempty"`
}

// ForgejoRepositoryStatus defines the observed state of ForgejoRepository.
type ForgejoRepositoryStatus struct {
	// Status is a short, human-readable summary of the reconciliation state,
	// e.g. "Ready" or "Error".
	// +optional
	Status string `json:"status,omitempty"`

	// Message contains additional detail when Status is not "Ready", such as
	// the last error encountered while talking to the Forgejo instance.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ForgejoRepository is the Schema for the forgejorepositories API.
type ForgejoRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoRepositorySpec   `json:"spec,omitempty"`
	Status ForgejoRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoRepositoryList contains a list of ForgejoRepository.
type ForgejoRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgejoRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ForgejoRepository{}, &ForgejoRepositoryList{})
}
