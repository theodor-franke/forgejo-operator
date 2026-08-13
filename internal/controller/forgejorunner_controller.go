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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

// runnerFinalizer is set on every ForgejoRunner so the operator can
// deregister the corresponding runner on Forgejo before the CR itself is
// removed from the cluster. The Secret and Deployment it creates are cleaned
// up by ordinary Kubernetes garbage collection via their owner references.
const runnerFinalizer = "forgejo.schranke.de/runner-finalizer"

const (
	// runnerSecretKey is the key, inside the Secret ensureRunnerSecret
	// creates, holding the runner's registration file content. Its name and
	// shape (WARNING/id/uuid/name/token/address/labels) deliberately match
	// forgejo-runner's own on-disk ".runner" file, so the init container in
	// the runner Deployment below only has to copy it into place -- no
	// registration step (CLI or otherwise) ever needs to run inside the
	// cluster.
	runnerSecretKey = ".runner"

	runnerFileWarning = "This file is managed by forgejo-operator. Do not edit it manually: " +
		"changes will be lost on the next reconcile, and removing it will orphan the registered runner on Forgejo."

	// defaultRunnerImage is used when ForgejoRunner.Spec.Image is unset.
	defaultRunnerImage = "data.forgejo.org/forgejo/runner:13"

	// defaultDindImage is used when ForgejoRunner.Spec.DindImage is unset.
	defaultDindImage = "docker:29-dind"

	runnerDataDir      = "/data"
	runnerDataVolume   = "runner-data"
	runnerSecretVolume = "runner-secret"

	// dindContainerName is the Docker-in-Docker sidecar job containers run
	// through, so job workloads never touch the node's own Docker daemon.
	dindContainerName = "dind"

	// dockerCertsVolume and dockerCertsDir hold the TLS material the dind
	// sidecar generates on startup and the runner container authenticates
	// with. TLS is not optional here even though the daemon is only ever
	// reached over the pod's loopback interface: containers in the same pod
	// share a network namespace, but that namespace's address is still the
	// pod IP, which is reachable from other pods on the cluster network
	// unless something else restricts it. Without TLS, the dind sidecar's
	// default insecure listener would hand out unauthenticated
	// root-equivalent Docker API access to anything that can reach the pod.
	dockerCertsVolume = "docker-certs"
	dockerCertsDir    = "/certs"

	// dockerHost points the runner at the dind sidecar over the pod's own
	// loopback interface -- both containers share a network namespace.
	dockerHost = "tcp://localhost:2376"

	// dindClientCertDir is where the dind sidecar writes the client half of
	// the TLS material it generates, and where the runner container mounts
	// it read-only to authenticate against dockerHost.
	dindClientCertDir = dockerCertsDir + "/client"

	// runnerConfigVolume/Dir/File hold the forgejo-runner config.yaml
	// generated when spec.dockerAccess is set (see runnerConfigYAML below).
	// Unlike the Secret/data volumes, this one is only mounted when needed --
	// forgejo-runner runs fine off pure defaults otherwise.
	runnerConfigVolume   = "runner-config"
	runnerConfigDir      = "/etc/forgejo-runner"
	runnerConfigFileName = "config.yaml"
)

// defaultRunnerLabels is used when ForgejoRunner.Spec.Labels is unset.
var defaultRunnerLabels = []string{"docker:docker://node:22-bookworm"}

// Default compute resources applied per container when the corresponding
// spec field is left unset, so every pod this controller creates has
// explicit requests/limits on every container even under a cluster
// ResourceQuota that requires them (a bare ResourceRequirements{} satisfies
// no such quota). Sized for typical CI workloads; spec.resources/
// spec.dindResources/spec.initResources override these per ForgejoRunner.
var (
	defaultRunnerContainerResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}

	// defaultDindResources is sized larger than the runner/init defaults
	// since job steps execute inside this container -- raise via
	// spec.dindResources for heavier jobs.
	defaultDindResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}

	defaultInitContainerResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
)

// resourcesOrDefault returns specified as-is if the caller set either
// Requests or Limits on it, otherwise defaults. Requests/Limits are checked
// together rather than merged field-by-field so that a caller who
// deliberately sets only one of the two (e.g. Limits without Requests) gets
// exactly that, not a mix of their value and the default's.
func resourcesOrDefault(specified, defaults corev1.ResourceRequirements) corev1.ResourceRequirements {
	if specified.Requests == nil && specified.Limits == nil {
		return defaults
	}
	return specified
}

// runnerRegistrationFile is the JSON shape forgejo-runner's daemon command
// reads from its working directory's ".runner" file (see the runner project:
// https://code.forgejo.org/forgejo/runner). Writing this directly, rather
// than shelling out to `forgejo-runner register`, is possible because
// Forgejo 15 added a plain REST endpoint (RegisterOrgRunner) that returns
// usable runner credentials in one call.
type runnerRegistrationFile struct {
	Warning string   `json:"WARNING"`
	ID      int64    `json:"id"`
	UUID    string   `json:"uuid"`
	Name    string   `json:"name"`
	Token   string   `json:"token"`
	Address string   `json:"address"`
	Labels  []string `json:"labels"`
}

// runnerConfigYAML is the forgejo-runner config.yaml generated when
// spec.dockerAccess is set. It puts job containers on the pod's network
// namespace (the same one the runner and dind containers already share) so
// they can reach dind at dockerHost exactly like the runner process itself
// does, and mounts dind's own client TLS cert into every job container so
// "docker"/"docker buildx" invocations inside job steps authenticate the
// same way. See forgejo-runner's own docs on Docker-in-Docker runner
// configuration for the runner.envs/container.valid_volumes/container.options
// fields this relies on.
func runnerConfigYAML() string {
	return fmt.Sprintf(`runner:
  envs:
    DOCKER_HOST: %s
    DOCKER_CERT_PATH: %s
    DOCKER_TLS_VERIFY: "1"
container:
  network: host
  valid_volumes:
    - %s
  options: -v %s:%s
`, dockerHost, dindClientCertDir, dindClientCertDir, dindClientCertDir, dindClientCertDir)
}

// runnerOrganizationNamespace returns the namespace runner's organizationRef
// should be resolved in: the explicit namespace if set, otherwise runner's
// own namespace, matching the same-namespace default used elsewhere in this
// API.
func runnerOrganizationNamespace(runner *forgejoschrankedev1alpha1.ForgejoRunner) string {
	if ns := runner.Spec.OrganizationRef.Namespace; ns != "" {
		return ns
	}
	return runner.Namespace
}

// ForgejoRunnerReconciler reconciles a ForgejoRunner object
type ForgejoRunnerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorunners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorunners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorunners/finalizers,verbs=update
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch

// Reconcile registers (once) a Forgejo Actions runner for the organization
// referenced by spec.organizationRef, storing its credentials in a Secret
// named after the ForgejoRunner object, and deploys a Deployment of runner
// pods that consume that Secret.
func (r *ForgejoRunnerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	runner := &forgejoschrankedev1alpha1.ForgejoRunner{}
	if err := r.Get(ctx, req.NamespacedName, runner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !runner.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, runner)
	}

	if !controllerutil.ContainsFinalizer(runner, runnerFinalizer) {
		controllerutil.AddFinalizer(runner, runnerFinalizer)
		if err := r.Update(ctx, runner); err != nil {
			return ctrl.Result{}, err
		}
	}

	org, err := resolveOrganization(ctx, r.Client, runnerOrganizationNamespace(runner), runner.Spec.OrganizationRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve ForgejoOrganization", "organization", runner.Spec.OrganizationRef.Name)
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve Forgejo instance", "instance", org.Spec.InstanceRef.Name)
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	secret, err := r.ensureRunnerSecret(ctx, apiClient, instance, org.Name, runner)
	if err != nil {
		log.Error(err, "unable to ensure runner registration secret")
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	runnerID, err := runnerIDFromSecret(secret)
	if err != nil {
		log.Error(err, "unable to read runner ID back from secret")
		return r.setStatus(ctx, runner, statusError, err.Error())
	}
	runner.Status.RunnerID = runnerID

	if err := r.reconcileConfig(ctx, runner); err != nil {
		log.Error(err, "unable to reconcile runner config")
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	if err := r.reconcileDeployment(ctx, runner); err != nil {
		log.Error(err, "unable to reconcile runner deployment")
		return r.setStatus(ctx, runner, statusError, err.Error())
	}

	return r.setStatus(ctx, runner, statusReady, "")
}

// ensureRunnerSecret makes sure a Secret named after runner exists, holding
// its Forgejo Actions runner registration. If it doesn't exist yet, a new
// runner is registered on Forgejo and its credentials are stored; an
// already-existing Secret is left untouched, since a runner's registration
// token cannot be retrieved again and re-registering would orphan the
// previous runner on Forgejo.
func (r *ForgejoRunnerReconciler) ensureRunnerSecret(
	ctx context.Context,
	apiClient *forgejoapi.Client,
	instance *forgejoschrankedev1alpha1.Forgejo,
	orgName string,
	runner *forgejoschrankedev1alpha1.ForgejoRunner,
) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Name: runner.Name, Namespace: runner.Namespace}, secret)
	if err == nil {
		return secret, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting runner secret %s/%s: %w", runner.Namespace, runner.Name, err)
	}

	labels := runner.Spec.Labels
	if len(labels) == 0 {
		labels = defaultRunnerLabels
	}

	reg, err := apiClient.RegisterOrgRunner(ctx, orgName, forgejoapi.RegisterRunnerOptions{
		Name: runner.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("registering runner on Forgejo: %w", err)
	}

	encoded, err := json.Marshal(runnerRegistrationFile{
		Warning: runnerFileWarning,
		ID:      reg.ID,
		UUID:    reg.UUID,
		Name:    runner.Name,
		Token:   reg.Token,
		Address: instance.Spec.Url,
		Labels:  labels,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding runner registration file: %w", err)
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: runner.Name, Namespace: runner.Namespace},
		Data:       map[string][]byte{runnerSecretKey: encoded},
	}
	if err := controllerutil.SetControllerReference(runner, secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference on runner secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("creating runner secret %s/%s: %w", runner.Namespace, runner.Name, err)
	}
	return secret, nil
}

// runnerIDFromSecret decodes the runner ID back out of a Secret created by
// ensureRunnerSecret, so status.runnerID stays accurate across reconciles
// (including after an operator restart) without needing a second place to
// store it.
func runnerIDFromSecret(secret *corev1.Secret) (int64, error) {
	var file runnerRegistrationFile
	if err := json.Unmarshal(secret.Data[runnerSecretKey], &file); err != nil {
		return 0, fmt.Errorf("decoding runner secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return file.ID, nil
}

// reconcileConfig creates/updates or removes the ConfigMap holding
// runner-config.yaml (see runnerConfigYAML) to match spec.dockerAccess,
// mirroring pullkey.go's create-or-delete-to-match-spec idiom for
// optOutDefaultPullKey. Left absent (not just empty) when dockerAccess is
// unset, so desiredRunnerDeploymentSpec never mounts a config volume that
// doesn't exist.
func (r *ForgejoRunnerReconciler) reconcileConfig(ctx context.Context, runner *forgejoschrankedev1alpha1.ForgejoRunner) error {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: runnerConfigName(runner), Namespace: runner.Namespace},
	}

	if !runner.Spec.DockerAccess {
		err := r.Delete(ctx, configMap)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting runner config %s/%s: %w", configMap.Namespace, configMap.Name, err)
		}
		return nil
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		configMap.Data = map[string]string{runnerConfigFileName: runnerConfigYAML()}
		return controllerutil.SetControllerReference(runner, configMap, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("reconciling runner config %s/%s: %w", configMap.Namespace, configMap.Name, err)
	}
	return nil
}

// runnerConfigName is the name of the ConfigMap reconcileConfig manages,
// distinct from runner.Name (already used for the registration Secret and
// the Deployment) so all three can coexist.
func runnerConfigName(runner *forgejoschrankedev1alpha1.ForgejoRunner) string {
	return runner.Name + "-config"
}

// reconcileDeployment creates or updates the Deployment running runner's
// pods, keeping it in sync with spec on every reconcile.
func (r *ForgejoRunnerReconciler) reconcileDeployment(ctx context.Context, runner *forgejoschrankedev1alpha1.ForgejoRunner) error {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: runner.Name, Namespace: runner.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Spec = desiredRunnerDeploymentSpec(runner)
		return controllerutil.SetControllerReference(runner, deployment, r.Scheme)
	})
	return err
}

// desiredRunnerDeploymentSpec builds the DeploymentSpec for runner's pods.
// An init container copies the ".runner" registration file out of the
// (read-only) Secret volume into a writable emptyDir, since forgejo-runner
// writes its own state (logs, cache) alongside that file. Job steps run
// through a Docker-in-Docker sidecar rather than the node's own Docker
// socket, so a job container can never reach (or be reached by) anything
// else on the node: deleting the pod throws away the sidecar's daemon and
// everything it ever ran, with nothing left behind on the node.
func desiredRunnerDeploymentSpec(runner *forgejoschrankedev1alpha1.ForgejoRunner) appsv1.DeploymentSpec {
	image := runner.Spec.Image
	if image == "" {
		image = defaultRunnerImage
	}
	dindImage := runner.Spec.DindImage
	if dindImage == "" {
		dindImage = defaultDindImage
	}

	initResources := resourcesOrDefault(runner.Spec.InitResources, defaultInitContainerResources)
	dindResources := resourcesOrDefault(runner.Spec.DindResources, defaultDindResources)
	runnerResources := resourcesOrDefault(runner.Spec.Resources, defaultRunnerContainerResources)

	podLabels := map[string]string{
		"app.kubernetes.io/name":     "forgejo-runner",
		"app.kubernetes.io/instance": runner.Name,
	}

	privileged := true

	runnerCommand := "until [ -f " + dindClientCertDir + "/cert.pem ]; do sleep 1; done; exec forgejo-runner daemon"
	runnerVolumeMounts := []corev1.VolumeMount{
		{Name: runnerDataVolume, MountPath: runnerDataDir},
		{Name: dockerCertsVolume, MountPath: dockerCertsDir, ReadOnly: true},
	}
	volumes := []corev1.Volume{
		{
			Name: runnerSecretVolume,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: runner.Name},
			},
		},
		{
			Name:         runnerDataVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name:         dockerCertsVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	if runner.Spec.DockerAccess {
		runnerCommand += " --config " + runnerConfigDir + "/" + runnerConfigFileName
		runnerVolumeMounts = append(runnerVolumeMounts,
			corev1.VolumeMount{Name: runnerConfigVolume, MountPath: runnerConfigDir, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{
			Name: runnerConfigVolume,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: runnerConfigName(runner)},
				},
			},
		})
	}

	return appsv1.DeploymentSpec{
		Replicas: runner.Spec.Replicas,
		Selector: &metav1.LabelSelector{MatchLabels: podLabels},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:      "copy-runner-registration",
						Image:     image,
						Command:   []string{"sh", "-c", "cp /secret/" + runnerSecretKey + " " + runnerDataDir + "/" + runnerSecretKey},
						Resources: initResources,
						VolumeMounts: []corev1.VolumeMount{
							{Name: runnerSecretVolume, MountPath: "/secret", ReadOnly: true},
							{Name: runnerDataVolume, MountPath: runnerDataDir},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:      dindContainerName,
						Image:     dindImage,
						Resources: dindResources,
						SecurityContext: &corev1.SecurityContext{
							Privileged: &privileged,
						},
						Env: []corev1.EnvVar{
							{Name: "DOCKER_TLS_CERTDIR", Value: dockerCertsDir},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: dockerCertsVolume, MountPath: dockerCertsDir},
						},
					},
					{
						Name:  "runner",
						Image: image,
						// The dind sidecar only finishes generating its TLS
						// material once its daemon is actually up, so wait
						// for the client cert it writes before starting the
						// runner daemon, rather than racing it.
						Command:    []string{"sh", "-c", runnerCommand},
						WorkingDir: runnerDataDir,
						Resources:  runnerResources,
						Env: []corev1.EnvVar{
							{Name: "DOCKER_HOST", Value: dockerHost},
							{Name: "DOCKER_CERT_PATH", Value: dindClientCertDir},
							{Name: "DOCKER_TLS_VERIFY", Value: "1"},
						},
						VolumeMounts: runnerVolumeMounts,
					},
				},
				Volumes: volumes,
			},
		},
	}
}

// reconcileDelete deregisters runner from Forgejo and then releases the
// finalizer so the ForgejoRunner object itself can be garbage collected
// (which in turn lets Kubernetes garbage collect its owned Secret and
// Deployment via their owner references).
func (r *ForgejoRunnerReconciler) reconcileDelete(ctx context.Context, runner *forgejoschrankedev1alpha1.ForgejoRunner) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(runner, runnerFinalizer) {
		return ctrl.Result{}, nil
	}

	if runner.Status.RunnerID != 0 {
		org, err := resolveOrganization(ctx, r.Client, runnerOrganizationNamespace(runner), runner.Spec.OrganizationRef.Name)
		switch {
		case apierrors.IsNotFound(err):
			log.Info("ForgejoOrganization no longer exists, skipping remote runner cleanup",
				"organization", runner.Spec.OrganizationRef.Name)
		case err != nil:
			return ctrl.Result{}, err
		default:
			instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
			switch {
			case apierrors.IsNotFound(err):
				log.Info("Forgejo instance no longer exists, skipping remote runner cleanup",
					"instance", org.Spec.InstanceRef.Name)
			case err != nil:
				return ctrl.Result{}, err
			default:
				apiClient, err := buildAPIClient(ctx, r.Client, instance)
				if err != nil {
					return ctrl.Result{}, err
				}
				if err := apiClient.DeleteOrgRunner(ctx, org.Name, runner.Status.RunnerID); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(runner, runnerFinalizer)
	if err := r.Update(ctx, runner); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ForgejoRunnerReconciler) setStatus(ctx context.Context, runner *forgejoschrankedev1alpha1.ForgejoRunner, status, message string) (ctrl.Result, error) {
	runner.Status.Status = status
	runner.Status.Message = message
	if err := r.Status().Update(ctx, runner); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoRunnerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.ForgejoRunner{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.Deployment{}).
		Named("forgejorunner").
		Complete(r)
}
