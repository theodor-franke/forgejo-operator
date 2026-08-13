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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ForgejoRunnerSpec defines the desired state of ForgejoRunner.
type ForgejoRunnerSpec struct {
	// OrganizationRef references the ForgejoOrganization this runner is
	// registered against. The runner picks up Actions jobs from every
	// repository in that organization. Unlike OrganizationRef elsewhere in
	// this API, this reference may cross namespaces: the runner's Secret and
	// Deployment are owned by (and always live in the same namespace as) the
	// ForgejoRunner CR itself, so letting the org lookup cross namespaces --
	// rather than the runner's own workload -- lets a ForgejoRunner be
	// created in a dedicated namespace (e.g. one with a privileged Pod
	// Security admission level, for the dind sidecar) without also having to
	// move its ForgejoOrganization there.
	OrganizationRef RunnerOrganizationRef `json:"organizationRef"`

	// Replicas is the number of runner pods to run. All replicas share the
	// same registration (Forgejo allows many runner processes to poll with
	// the same runner credentials).
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Labels are the Forgejo Actions runner labels this runner registers
	// with, e.g. "docker:docker://node:20-bookworm" -- they determine which
	// "runs-on" values in a workflow this runner accepts, and, for
	// docker://-prefixed labels, which image a job step runs in. Defaults to
	// a single generic "docker" label if unset.
	// +optional
	Labels []string `json:"labels,omitempty"`

	// Image is the forgejo-runner container image to deploy. Defaults to the
	// upstream data.forgejo.org/forgejo/runner image if unset.
	// +optional
	Image string `json:"image,omitempty"`

	// DindImage is the Docker-in-Docker sidecar image job containers run
	// through, keeping job workloads isolated from the node's own Docker
	// daemon instead of mounting its socket. Defaults to the upstream
	// docker:*-dind image if unset.
	// +optional
	DindImage string `json:"dindImage,omitempty"`

	// Resources sets compute resource requirements for the runner container,
	// which only polls for jobs and orchestrates the dind sidecar -- it does
	// not execute job steps itself. Defaults to a modest fixed size if unset,
	// so the pod has explicit requests/limits on every container even under a
	// cluster ResourceQuota that requires them.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// DindResources sets compute resource requirements for the
	// Docker-in-Docker sidecar container, which is where job steps actually
	// execute -- size this for the heaviest job this runner is expected to
	// run. Defaults to a fixed size suitable for light CI workloads if unset,
	// so the pod has explicit requests/limits on every container even under a
	// cluster ResourceQuota that requires them.
	// +optional
	DindResources corev1.ResourceRequirements `json:"dindResources,omitempty"`

	// InitResources sets compute resource requirements for the init
	// container that copies the runner's registration file into place before
	// the runner and dind containers start. Defaults to a small fixed size if
	// unset, since this container only performs a single file copy, so the
	// pod has explicit requests/limits on every container even under a
	// cluster ResourceQuota that requires them.
	// +optional
	InitResources corev1.ResourceRequirements `json:"initResources,omitempty"`

	// DockerAccess, if true, makes the dind sidecar's Docker daemon reachable
	// from *inside* job containers (e.g. so a "docker buildx build" workflow
	// step works), not just from the runner container that spawns them.
	// Without this, job containers have no Docker socket or DOCKER_HOST at
	// all: only the runner process itself can talk to dind. Enabling it
	// generates a forgejo-runner config.yaml that puts every job container on
	// the pod's network namespace (container.network: host) and points
	// DOCKER_HOST/DOCKER_CERT_PATH at the dind sidecar, so any job step this
	// runner executes gets full Docker API access to that sidecar -- treat it
	// like handing every workflow root on the pod's dind daemon. Defaults to
	// false.
	// +optional
	DockerAccess bool `json:"dockerAccess,omitempty"`
}

// RunnerOrganizationRef references a ForgejoOrganization by name, optionally
// in a different namespace than the object doing the referencing. Namespace
// defaults to that object's own namespace when unset.
type RunnerOrganizationRef struct {
	Name string `json:"name"`

	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ForgejoRunnerStatus defines the observed state of ForgejoRunner.
type ForgejoRunnerStatus struct {
	// Status is a short, human-readable summary of the reconciliation state,
	// e.g. "Ready" or "Error".
	// +optional
	Status string `json:"status,omitempty"`

	// Message contains additional detail when Status is not "Ready", such as
	// the last error encountered while talking to the Forgejo instance.
	// +optional
	Message string `json:"message,omitempty"`

	// RunnerID is the ID Forgejo assigned this runner at registration time.
	// +optional
	RunnerID int64 `json:"runnerID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`
// +kubebuilder:printcolumn:name="Runner ID",type=integer,JSONPath=`.status.runnerID`

// ForgejoRunner is the Schema for the forgejorunners API.
type ForgejoRunner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ForgejoRunnerSpec   `json:"spec,omitempty"`
	Status ForgejoRunnerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ForgejoRunnerList contains a list of ForgejoRunner.
type ForgejoRunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ForgejoRunner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ForgejoRunner{}, &ForgejoRunnerList{})
}
