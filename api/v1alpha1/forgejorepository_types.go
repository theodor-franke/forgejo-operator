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

	// DeleteBranchAfterMerge pre-checks "Delete branch after merge" on this
	// repository's pull requests. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	DeleteBranchAfterMerge *bool `json:"deleteBranchAfterMerge,omitempty"`

	// OptOutDefaultPullKey skips adding the Forgejo instance's shared
	// default deploy key (see Forgejo.spec.defaultPullKeySecretRef) to this
	// repository. Has no effect if the instance doesn't have one configured.
	// +optional
	OptOutDefaultPullKey bool `json:"optOutDefaultPullKey,omitempty"`

	// DeployKey provisions a read-only, repo-scoped SSH deploy key and
	// publishes it as a Secret in this repository's own namespace, for
	// use as a Flux GitRepository's spec.secretRef (which only supports
	// same-namespace secrets). Defaults to disabled for backward compat.
	// +optional
	DeployKey *DeployKeySpec `json:"deployKey,omitempty"`

	// Sops provisions a SOPS age keypair for this repository: the private key
	// is published as a Secret in this repository's own namespace (for use as
	// a Flux Kustomization's spec.decryption.secretRef), the public key as a
	// ConfigMap in the same namespace, and a .sops.yaml file referencing the
	// public key is committed to the repository's default branch. Defaults to
	// disabled for backward compat.
	// +optional
	Sops *SopsSpec `json:"sops,omitempty"`
}

// DeployKeySpec configures automatic provisioning of a read-only,
// single-repo-scoped SSH deploy key, published as a Secret in the
// referencing ForgejoRepository's own namespace. Unlike
// Forgejo.spec.defaultPullKeySecretRef's shared, org-wide key, this key can
// only ever read the one repository it was created for, so publishing it as
// a same-namespace Secret costs nothing even in a multi-tenant cluster.
type DeployKeySpec struct {
	// Enabled provisions the deploy key and Secret when true.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretName overrides the generated Secret's name. Defaults to
	// "<repository-name>-deploy-key" when unset. The Secret carries the
	// same "identity"/"identity.pub"/"known_hosts" keys as
	// Forgejo.spec.defaultPullKeySecretRef's Secret (Flux's GitRepository
	// SSH auth secretRef convention).
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// SopsSpec configures automatic provisioning of a SOPS age keypair for
// decrypting secrets in this repository, published as a Secret +
// ConfigMap pair in the referencing ForgejoRepository's own namespace, with
// the public key also committed to the repository as .sops.yaml so project
// owners can encrypt new secrets without needing read access to the Secret.
type SopsSpec struct {
	// Enabled provisions the age keypair, Secret, ConfigMap, and committed
	// .sops.yaml when true.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretName overrides the generated Secret's name. Defaults to
	// "sops-age" when unset — this matches the fixed name
	// cluster-projects' Flux Kustomizations already reference as
	// spec.decryption.secretRef.name, so leave this unset unless you know
	// you need to change it.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// PublicKeyConfigMapName overrides the generated ConfigMap's name.
	// Defaults to "sops-age-public-key" when unset.
	// +optional
	PublicKeyConfigMapName string `json:"publicKeyConfigMapName,omitempty"`
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

	// DeployKeyID is the ID of the repo-scoped deploy key on the Forgejo
	// repository created for spec.deployKey, so it can be targeted
	// precisely for removal when spec.deployKey is disabled or the
	// ForgejoRepository is deleted.
	// +optional
	DeployKeyID int64 `json:"deployKeyID,omitempty"`

	// DeployKeySecretName is the name of the Secret spec.deployKey last
	// published the provisioned SSH keypair as, so it can still be found
	// and removed if spec.deployKey.secretName changes or spec.deployKey is
	// removed entirely.
	// +optional
	DeployKeySecretName string `json:"deployKeySecretName,omitempty"`

	// SopsSecretName is the name of the Secret spec.sops last published the
	// age keypair as, so it can still be found and removed if
	// spec.sops.secretName changes or spec.sops is removed entirely.
	// +optional
	SopsSecretName string `json:"sopsSecretName,omitempty"`

	// SopsPublicKeyConfigMapName is the name of the ConfigMap spec.sops last
	// published the public key as, so it can still be found and removed if
	// spec.sops.publicKeyConfigMapName changes or spec.sops is removed
	// entirely.
	// +optional
	SopsPublicKeyConfigMapName string `json:"sopsPublicKeyConfigMapName,omitempty"`
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
