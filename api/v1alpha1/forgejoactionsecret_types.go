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

// ForgejoActionSecretSpec defines the desired state of ForgejoActionSecret.
type ForgejoActionSecretSpec struct {
	// OrganizationRef references the ForgejoOrganization this action secret
	// belongs to. Defaults to this object's own namespace when
	// organizationRef.namespace is unset.
	OrganizationRef OrganizationRef `json:"organizationRef"`

	// Name is the action secret's name on Forgejo, e.g. DOCKER_PASSWORD. It
	// must start with a letter or underscore and contain only letters,
	// digits, and underscores; Forgejo uppercases it regardless of the case
	// used here.
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`

	// ValueFrom sources the action secret's value from a key in a
	// Kubernetes Secret.
	ValueFrom ValueFromSecret `json:"valueFrom"`
}

// OrganizationRef references a ForgejoOrganization by name, optionally in a
// different namespace than the object doing the referencing. Namespace
// defaults to that object's own namespace when unset.
type OrganizationRef struct {
	Name string `json:"name"`

	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ValueFromSecret references a single key in a namespaced Kubernetes Secret.
type ValueFromSecret struct {
	SecretName      string `json:"secretName"`
	SecretNamespace string `json:"secretNamespace"`
	Key             string `json:"key"`
}

// ForgejoActionSecretStatus defines the observed state of ForgejoActionSecret.
type ForgejoActionSecretStatus struct {
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

// ForgejoActionSecret is the Schema for the forgejoactionsecrets API.
type ForgejoActionSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoActionSecretSpec   `json:"spec,omitempty"`
	Status ForgejoActionSecretStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoActionSecretList contains a list of ForgejoActionSecret.
type ForgejoActionSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgejoActionSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ForgejoActionSecret{}, &ForgejoActionSecretList{})
}
