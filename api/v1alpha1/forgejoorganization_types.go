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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ForgejoOrganizationSpec defines the desired state of ForgejoOrganization.
type ForgejoOrganizationSpec struct {
	// InstanceRef references the Forgejo instance this organization should be
	// created on, by the name of a cluster-scoped Forgejo resource.
	InstanceRef InstanceRef `json:"instanceRef"`

	// Visibility controls who can see the organization.
	// +kubebuilder:validation:Enum=public;limited;private
	// +kubebuilder:default=public
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// AdminPermissionRights allows repository administrators to add/remove team
	// members and change team access without being an organization owner.
	// +optional
	AdminPermissionRights bool `json:"adminPermissionRights,omitempty"`

	// Teams are created (and kept in sync) on the organization. Each team's
	// Groups are also aggregated, across every ForgejoOrganization pointing at
	// the same instance, into the claimed-group-to-organization-team mapping
	// configured on that instance's first OAuth2/OIDC authentication source.
	// +optional
	Teams []Team `json:"teams,omitempty"`
}

type Team struct {
	// Name is both the team's name in Kubernetes and its name on Forgejo.
	Name string `json:"name"`

	// Description is shown on the team's page in Forgejo.
	// +optional
	Description string `json:"description,omitempty"`

	// Groups are the names of IdP-claimed groups that should be mapped to this
	// team via the instance's authentication source (see ForgejoOrganizationSpec.Teams).
	// +optional
	Groups []string `json:"groups,omitempty"`

	// AccessMode selects between Forgejo's two team permission modes:
	// "custom" (the default), where RepoPermissions grants access
	// per repository feature, or "admin", where members get full
	// administrator access to every feature of every team repository
	// (including deleting repositories and managing collaborators) and
	// RepoPermissions is ignored.
	// +kubebuilder:validation:Enum=custom;admin
	// +kubebuilder:default=custom
	// +optional
	AccessMode string `json:"accessMode,omitempty"`

	// RepoPermissions sets the team's per-feature repository access when
	// AccessMode is "custom" (the default). Every field defaults to "none".
	// +optional
	RepoPermissions RepoPermissions `json:"repoPermissions,omitempty"`

	// CanCreateOrgRepo allows members of this team to create new repositories
	// in the organization.
	// +optional
	CanCreateOrgRepo bool `json:"canCreateOrgRepo,omitempty"`

	// IncludesAllRepositories gives the team access to all current and future
	// repositories in the organization, rather than only explicitly assigned ones.
	// +optional
	IncludesAllRepositories bool `json:"includesAllRepositories,omitempty"`
}

// RepoPermissions is the per-feature repository access level for a "custom"
// access team, mirroring the three-way "No access" / "Read" / "Write" table
// Forgejo's own team edit page shows for each feature.
type RepoPermissions struct {
	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Code string `json:"code,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Issues string `json:"issues,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	PullRequests string `json:"pullRequests,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Releases string `json:"releases,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Wiki string `json:"wiki,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Projects string `json:"projects,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Packages string `json:"packages,omitempty"`

	// +kubebuilder:validation:Enum=none;read;write
	// +kubebuilder:default=none
	// +optional
	Actions string `json:"actions,omitempty"`
}

// ForgejoOrganizationStatus defines the observed state of ForgejoOrganization.
type ForgejoOrganizationStatus struct {
	// Status is a short, human-readable summary of the reconciliation state,
	// e.g. "Ready" or "Error".
	// +optional
	Status string `json:"status,omitempty"`

	// Message contains additional detail when Status is not "Ready", such as the
	// last error encountered while talking to the Forgejo instance.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ForgejoOrganization is the Schema for the forgejoorganizations API.
type ForgejoOrganization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoOrganizationSpec   `json:"spec,omitempty"`
	Status ForgejoOrganizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoOrganizationList contains a list of ForgejoOrganization.
type ForgejoOrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgejoOrganization `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ForgejoOrganization{}, &ForgejoOrganizationList{})
}
