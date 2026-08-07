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

// ForgejoSpec defines the desired state of Forgejo.
type ForgejoSpec struct {
	// Url is the base URL of the already-running Forgejo instance to manage,
	// e.g. https://forgejo.example.com.
	Url string `json:"url"`

	// AuthSecretRef points at the Secret holding credentials used to authenticate
	// against the Forgejo instance's API.
	AuthSecretRef AuthSecretRef `json:"authRef"`

	// DefaultPullKeySecretRef references the Secret holding a shared SSH
	// deploy key pair that gets registered as a read-only deploy key on
	// every ForgejoRepository targeting this instance (a repository can opt
	// out via its own spec.optOutDefaultPullKey). If the Secret does not
	// already exist, the operator generates a new ed25519 keypair and
	// creates it, with keys "publicKey" (OpenSSH authorized_keys format),
	// "privateKey" (OpenSSH PEM format), and "knownHosts" (this instance's
	// SSH host key, discovered via SSHHost/SSHPort, in known_hosts format).
	// Leave unset to not manage a default pull key for this instance.
	// +optional
	DefaultPullKeySecretRef *PullKeySecretRef `json:"defaultPullKeySecretRef,omitempty"`

	// SSHHost is the hostname Forgejo's SSH git server is reachable at.
	// Only used to discover the server's SSH host key when
	// DefaultPullKeySecretRef is set and its Secret needs to be created.
	// Defaults to the hostname portion of Url.
	// +optional
	SSHHost string `json:"sshHost,omitempty"`

	// SSHPort is the port Forgejo's SSH git server listens on. Only used
	// alongside SSHHost. Defaults to 22.
	// +optional
	SSHPort int32 `json:"sshPort,omitempty"`

	// Base is the externally reachable base URL of the Flux
	// notification-controller webhook receiver endpoint (e.g.
	// https://flux-webhook.example.com), used by ForgejoWebhook to combine
	// with a referenced Receiver's status.webhookPath into the full webhook
	// URL registered on this instance. Required for any ForgejoWebhook
	// targeting this instance to reconcile successfully.
	// +optional
	Base string `json:"base,omitempty"`
}

// PullKeySecretRef references a namespaced Secret holding a shared SSH
// deploy key pair.
type PullKeySecretRef struct {
	SecretName      string `json:"secretName"`
	SecretNamespace string `json:"secretNamespace"`
}

// AuthSecretRef references a namespaced Secret containing basic-auth credentials
// for a Forgejo instance.
type AuthSecretRef struct {
	SecretName      string `json:"secretName"`
	SecretNamespace string `json:"secretNamespace"`
	UsernameKey     string `json:"usernameKey"`
	PasswordKey     string `json:"passwordKey"`
}

type InstanceRef struct {
	Name string `json:"name"`
}

// ForgejoStatus defines the observed state of Forgejo.
type ForgejoStatus struct {
	// Status is a short, human-readable summary of the reconciliation state,
	// e.g. "Ready" or "Error".
	// +optional
	Status string `json:"status,omitempty"`

	// Version is the Forgejo server version reported by the instance's info endpoint.
	// +optional
	Version string `json:"version,omitempty"`

	// Message contains additional detail when Status is not "Ready", such as the
	// last error encountered while talking to the instance.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Forgejo is the Schema for the forgejoes API.
type Forgejo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoSpec   `json:"spec,omitempty"`
	Status ForgejoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoList contains a list of Forgejo.
type ForgejoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Forgejo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Forgejo{}, &ForgejoList{})
}
