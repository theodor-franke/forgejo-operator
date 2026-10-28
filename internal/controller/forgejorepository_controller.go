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
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/agekey"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
	"github.com/theodor-franke/forgejo-operator/internal/sshkey"
)

// repositoryFinalizer is set on every ForgejoRepository so the operator can
// delete the corresponding repository in Forgejo before the CR itself is
// removed from the cluster.
const repositoryFinalizer = "forgejo.schranke.de/repository-finalizer"

const (
	// sopsSecretDataField is the spec.sops Secret key holding the age
	// private key. Flux's Kustomization decryption picks up any key ending
	// in ".agekey".
	sopsSecretDataField = "age.agekey"
	// sopsPublicKeyConfigMapKey is the spec.sops ConfigMap key holding the
	// age public key.
	sopsPublicKeyConfigMapKey = "age.pub"
	// sopsFilePath is where spec.sops commits its .sops.yaml in the
	// repository.
	sopsFilePath = ".sops.yaml"
	// sopsDefaultBranch is the branch .sops.yaml is committed to when
	// spec.defaultBranch is unset.
	sopsDefaultBranch = "main"
)

// ForgejoRepositoryReconciler reconciles a ForgejoRepository object
type ForgejoRepositoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorepositories/finalizers,verbs=update
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete

// Reconcile creates (and keeps in sync) a repository on the organization
// referenced by spec.organizationRef. The repository's name on Forgejo is
// the ForgejoRepository object's own metadata.name.
func (r *ForgejoRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	repo := &forgejoschrankedev1alpha1.ForgejoRepository{}
	if err := r.Get(ctx, req.NamespacedName, repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !repo.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, repo)
	}

	if !controllerutil.ContainsFinalizer(repo, repositoryFinalizer) {
		controllerutil.AddFinalizer(repo, repositoryFinalizer)
		if err := r.Update(ctx, repo); err != nil {
			return ctrl.Result{}, err
		}
	}

	org, err := resolveOrganization(ctx, r.Client, organizationRefNamespace(repo.Spec.OrganizationRef, repo.Namespace), repo.Spec.OrganizationRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve ForgejoOrganization", "organization", repo.Spec.OrganizationRef.Name)
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve Forgejo instance", "instance", org.Spec.InstanceRef.Name)
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	if err := reconcileRemoteRepo(ctx, apiClient, org.Name, repo); err != nil {
		log.Error(err, "unable to reconcile repository on Forgejo instance")
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	if err := reconcileDefaultPullKey(ctx, r.Client, apiClient, instance, org.Name, repo.Name, repo.Spec.OptOutDefaultPullKey); err != nil {
		log.Error(err, "unable to reconcile default pull key on repository")
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	if err := r.reconcileDeployKeyState(ctx, apiClient, instance, org.Name, repo); err != nil {
		log.Error(err, "unable to reconcile repo-scoped deploy key")
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	if err := r.reconcileSopsState(ctx, apiClient, org.Name, repo); err != nil {
		log.Error(err, "unable to reconcile SOPS age key")
		return r.setStatus(ctx, repo, statusError, err.Error())
	}

	return r.setStatus(ctx, repo, statusReady, "")
}

// reconcileDeployKeyState provisions or removes repo's repo-scoped deploy
// key per spec.deployKey, keeping status.deployKeyID/status.deployKeySecretName
// in sync so both directions of the enabled/disabled transition are
// idempotent.
func (r *ForgejoRepositoryReconciler) reconcileDeployKeyState(ctx context.Context, apiClient *forgejoapi.Client, instance *forgejoschrankedev1alpha1.Forgejo, owner string, repo *forgejoschrankedev1alpha1.ForgejoRepository) error {
	if repo.Spec.DeployKey != nil && repo.Spec.DeployKey.Enabled {
		secretName := deployKeySecretName(repo)
		id, err := r.reconcileDeployKey(ctx, apiClient, instance, owner, repo, secretName)
		if err != nil {
			return err
		}
		repo.Status.DeployKeyID = id
		repo.Status.DeployKeySecretName = secretName
		return nil
	}

	if repo.Status.DeployKeyID == 0 {
		return nil
	}

	// spec.deployKey was disabled after being enabled (or removed
	// entirely): revoke the repo-scoped key and remove its Secret rather
	// than leaving a live, unused credential behind.
	if err := apiClient.DeleteDeployKey(ctx, owner, repo.Name, repo.Status.DeployKeyID); err != nil {
		return fmt.Errorf("removing disabled repo-scoped deploy key: %w", err)
	}

	secretName := repo.Status.DeployKeySecretName
	if secretName == "" {
		secretName = deployKeySecretName(repo)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: repo.Namespace}}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing disabled repo-scoped deploy key secret %s/%s: %w", repo.Namespace, secretName, err)
	}

	repo.Status.DeployKeyID = 0
	repo.Status.DeployKeySecretName = ""
	return nil
}

// deployKeySecretName returns the name spec.deployKey's Secret should be
// created/looked up under: an explicit spec.deployKey.secretName if set,
// otherwise "<repository-name>-deploy-key".
func deployKeySecretName(repo *forgejoschrankedev1alpha1.ForgejoRepository) string {
	if repo.Spec.DeployKey != nil && repo.Spec.DeployKey.SecretName != "" {
		return repo.Spec.DeployKey.SecretName
	}
	return repo.Name + "-deploy-key"
}

// deployKeyTitle identifies a repo-scoped deploy key on Forgejo, so it can
// be found again by listing (e.g. after an operator restart before
// status.deployKeyID was persisted) instead of relying solely on local
// state.
func deployKeyTitle(namespace string) string {
	return "flux-" + namespace
}

// reconcileDeployKey ensures repo has a read-only, repo-scoped SSH deploy
// key registered on Forgejo, generating and publishing a new keypair as a
// Secret named secretName in repo's own namespace if one doesn't already
// exist. It returns the deploy key's Forgejo-side ID.
func (r *ForgejoRepositoryReconciler) reconcileDeployKey(ctx context.Context, apiClient *forgejoapi.Client, instance *forgejoschrankedev1alpha1.Forgejo, owner string, repo *forgejoschrankedev1alpha1.ForgejoRepository, secretName string) (int64, error) {
	getErr := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: repo.Namespace}, &corev1.Secret{})
	var publicKey string
	switch {
	case getErr == nil:
		key, err := fetchSecretKey(ctx, r.Client, repo.Namespace, secretName, pullKeySecretPublicKeyField)
		if err != nil {
			return 0, err
		}
		publicKey = key
	case apierrors.IsNotFound(getErr):
		key, err := r.createDeployKeySecret(ctx, instance, repo, secretName)
		if err != nil {
			return 0, err
		}
		publicKey = key
	default:
		return 0, fmt.Errorf("getting deploy key secret %s/%s: %w", repo.Namespace, secretName, getErr)
	}
	publicKey = strings.TrimSpace(publicKey)

	keys, err := apiClient.ListDeployKeys(ctx, owner, repo.Name)
	if err != nil {
		return 0, fmt.Errorf("listing deploy keys: %w", err)
	}

	title := deployKeyTitle(repo.Namespace)
	for i := range keys {
		if keys[i].Title == title || keys[i].Key == publicKey {
			return keys[i].ID, nil
		}
	}

	created, err := apiClient.CreateDeployKey(ctx, owner, repo.Name, forgejoapi.CreateDeployKeyOptions{
		Title:    title,
		Key:      publicKey,
		ReadOnly: true,
	})
	if err != nil {
		return 0, fmt.Errorf("adding repo-scoped deploy key: %w", err)
	}
	return created.ID, nil
}

// createDeployKeySecret generates a new ed25519 deploy-key pair and
// discovers instance's SSH host key, then creates the Secret spec.deployKey
// publishes it as, owned by repo so ordinary Kubernetes garbage collection
// removes it when the ForgejoRepository is deleted. Returns the generated
// public key.
func (r *ForgejoRepositoryReconciler) createDeployKeySecret(ctx context.Context, instance *forgejoschrankedev1alpha1.Forgejo, repo *forgejoschrankedev1alpha1.ForgejoRepository, secretName string) (string, error) {
	host, port, err := sshHostPort(instance)
	if err != nil {
		return "", err
	}

	pair, err := sshkey.Generate(fmt.Sprintf("%s/%s", repo.Namespace, repo.Name))
	if err != nil {
		return "", fmt.Errorf("generating repo-scoped deploy key: %w", err)
	}

	knownHosts, err := sshkey.ScanHostKey(ctx, host, port)
	if err != nil {
		return "", fmt.Errorf("discovering SSH host key for %s:%d: %w", host, port, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: repo.Namespace},
		StringData: map[string]string{
			pullKeySecretPublicKeyField:  pair.PublicKey,
			pullKeySecretPrivateKeyField: pair.PrivateKey,
			pullKeySecretKnownHostsField: knownHosts,
		},
	}
	if err := controllerutil.SetControllerReference(repo, secret, r.Scheme); err != nil {
		return "", fmt.Errorf("setting owner reference on deploy key secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil {
		return "", fmt.Errorf("creating deploy key secret %s/%s: %w", repo.Namespace, secretName, err)
	}
	return pair.PublicKey, nil
}

// reconcileSopsState provisions or removes repo's SOPS age keypair Secret
// and public-key ConfigMap per spec.sops, keeping
// status.sopsSecretName/status.sopsPublicKeyConfigMapName in sync so both
// directions of the enabled/disabled transition are idempotent. The
// committed .sops.yaml is never removed: unlike a deploy key it isn't a
// live credential, and rewriting git history on disable would be a
// surprising, unrequested repository mutation.
func (r *ForgejoRepositoryReconciler) reconcileSopsState(ctx context.Context, apiClient *forgejoapi.Client, owner string, repo *forgejoschrankedev1alpha1.ForgejoRepository) error {
	if repo.Spec.Sops != nil && repo.Spec.Sops.Enabled {
		secretName, configMapName := sopsSecretName(repo), sopsConfigMapName(repo)

		// The name was changed while enabled: drop the objects published
		// under the old name rather than leaving them orphaned.
		if old := repo.Status.SopsSecretName; old != "" && old != secretName {
			if err := r.deleteSopsObject(ctx, &corev1.Secret{}, repo.Namespace, old); err != nil {
				return err
			}
		}
		if old := repo.Status.SopsPublicKeyConfigMapName; old != "" && old != configMapName {
			if err := r.deleteSopsObject(ctx, &corev1.ConfigMap{}, repo.Namespace, old); err != nil {
				return err
			}
		}

		publicKey, err := r.reconcileSopsSecret(ctx, repo, secretName)
		if err != nil {
			return err
		}
		if err := r.reconcileSopsConfigMap(ctx, repo, configMapName, publicKey); err != nil {
			return err
		}
		if err := reconcileSopsFile(ctx, apiClient, owner, repo, publicKey); err != nil {
			return err
		}

		repo.Status.SopsSecretName = secretName
		repo.Status.SopsPublicKeyConfigMapName = configMapName
		return nil
	}

	if repo.Status.SopsSecretName == "" && repo.Status.SopsPublicKeyConfigMapName == "" {
		return nil
	}

	// spec.sops was disabled after being enabled (or removed entirely):
	// remove the private key Secret and public key ConfigMap.
	if name := repo.Status.SopsSecretName; name != "" {
		if err := r.deleteSopsObject(ctx, &corev1.Secret{}, repo.Namespace, name); err != nil {
			return err
		}
	}
	if name := repo.Status.SopsPublicKeyConfigMapName; name != "" {
		if err := r.deleteSopsObject(ctx, &corev1.ConfigMap{}, repo.Namespace, name); err != nil {
			return err
		}
	}

	repo.Status.SopsSecretName = ""
	repo.Status.SopsPublicKeyConfigMapName = ""
	return nil
}

// deleteSopsObject deletes the spec.sops Secret or ConfigMap (obj selects
// which) named name in namespace, tolerating it already being gone.
func (r *ForgejoRepositoryReconciler) deleteSopsObject(ctx context.Context, obj client.Object, namespace, name string) error {
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing SOPS %T %s/%s: %w", obj, namespace, name, err)
	}
	return nil
}

// sopsSecretName returns the name spec.sops's Secret should be
// created/looked up under: an explicit spec.sops.secretName if set,
// otherwise the fixed "sops-age". Deliberately not derived from the
// repository name: Flux Kustomizations referencing it as
// spec.decryption.secretRef.name commonly hardcode this literal.
func sopsSecretName(repo *forgejoschrankedev1alpha1.ForgejoRepository) string {
	if repo.Spec.Sops != nil && repo.Spec.Sops.SecretName != "" {
		return repo.Spec.Sops.SecretName
	}
	return "sops-age"
}

// sopsConfigMapName returns the name spec.sops's public-key ConfigMap
// should be created/looked up under: an explicit
// spec.sops.publicKeyConfigMapName if set, otherwise "sops-age-public-key".
func sopsConfigMapName(repo *forgejoschrankedev1alpha1.ForgejoRepository) string {
	if repo.Spec.Sops != nil && repo.Spec.Sops.PublicKeyConfigMapName != "" {
		return repo.Spec.Sops.PublicKeyConfigMapName
	}
	return "sops-age-public-key"
}

// reconcileSopsSecret ensures the age private key Secret named secretName
// exists in repo's namespace, generating a new keypair only if it doesn't
// (an existing key is never rotated). It returns the public key, always
// re-derived from the Secret's private key so the Secret stays the single
// source of truth that the ConfigMap and .sops.yaml follow.
func (r *ForgejoRepositoryReconciler) reconcileSopsSecret(ctx context.Context, repo *forgejoschrankedev1alpha1.ForgejoRepository, secretName string) (string, error) {
	getErr := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: repo.Namespace}, &corev1.Secret{})
	switch {
	case getErr == nil:
		privateKey, err := fetchSecretKey(ctx, r.Client, repo.Namespace, secretName, sopsSecretDataField)
		if err != nil {
			return "", err
		}
		publicKey, err := agekey.PublicKey(privateKey)
		if err != nil {
			return "", fmt.Errorf("reading SOPS age key from secret %s/%s: %w", repo.Namespace, secretName, err)
		}
		return publicKey, nil
	case apierrors.IsNotFound(getErr):
		pair, err := agekey.Generate()
		if err != nil {
			return "", fmt.Errorf("generating SOPS age key: %w", err)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: repo.Namespace},
			StringData: map[string]string{sopsSecretDataField: pair.PrivateKey},
		}
		if err := controllerutil.SetControllerReference(repo, secret, r.Scheme); err != nil {
			return "", fmt.Errorf("setting owner reference on SOPS age key secret: %w", err)
		}
		if err := r.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("creating SOPS age key secret %s/%s: %w", repo.Namespace, secretName, err)
		}
		return pair.PublicKey, nil
	default:
		return "", fmt.Errorf("getting SOPS age key secret %s/%s: %w", repo.Namespace, secretName, getErr)
	}
}

// reconcileSopsConfigMap ensures the ConfigMap named configMapName in repo's
// namespace publishes publicKey, creating it (owned by repo) if missing or
// correcting it if it drifted from the Secret.
func (r *ForgejoRepositoryReconciler) reconcileSopsConfigMap(ctx context.Context, repo *forgejoschrankedev1alpha1.ForgejoRepository, configMapName, publicKey string) error {
	configMap := &corev1.ConfigMap{}
	getErr := r.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: repo.Namespace}, configMap)
	switch {
	case getErr == nil:
		if configMap.Data[sopsPublicKeyConfigMapKey] == publicKey {
			return nil
		}
		if configMap.Data == nil {
			configMap.Data = map[string]string{}
		}
		configMap.Data[sopsPublicKeyConfigMapKey] = publicKey
		if err := r.Update(ctx, configMap); err != nil {
			return fmt.Errorf("updating SOPS public key configmap %s/%s: %w", repo.Namespace, configMapName, err)
		}
		return nil
	case apierrors.IsNotFound(getErr):
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: repo.Namespace},
			Data:       map[string]string{sopsPublicKeyConfigMapKey: publicKey},
		}
		if err := controllerutil.SetControllerReference(repo, configMap, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on SOPS public key configmap: %w", err)
		}
		if err := r.Create(ctx, configMap); err != nil {
			return fmt.Errorf("creating SOPS public key configmap %s/%s: %w", repo.Namespace, configMapName, err)
		}
		return nil
	default:
		return fmt.Errorf("getting SOPS public key configmap %s/%s: %w", repo.Namespace, configMapName, getErr)
	}
}

// sopsFileContent is the .sops.yaml spec.sops commits for publicKey. It
// deliberately has no path_regex, so the rule applies to every file.
func sopsFileContent(publicKey string) string {
	return fmt.Sprintf("creation_rules:\n  - age: %s\n", publicKey)
}

// reconcileSopsFile ensures .sops.yaml on repo's default branch (or
// sopsDefaultBranch if unset) references publicKey, committing it if missing
// and updating it if it differs. This needs the branch to already exist, so
// a repository created without autoInit errors here until something is
// pushed, and is retried on a later reconcile.
func reconcileSopsFile(ctx context.Context, apiClient *forgejoapi.Client, owner string, repo *forgejoschrankedev1alpha1.ForgejoRepository, publicKey string) error {
	branch := repo.Spec.DefaultBranch
	if branch == "" {
		branch = sopsDefaultBranch
	}
	desired := sopsFileContent(publicKey)
	encoded := base64.StdEncoding.EncodeToString([]byte(desired))

	existing, err := apiClient.GetContents(ctx, owner, repo.Name, sopsFilePath, branch)
	switch {
	case errors.Is(err, forgejoapi.ErrNotFound):
		if err := apiClient.CreateFile(ctx, owner, repo.Name, sopsFilePath, forgejoapi.CreateFileOptions{
			Content: encoded,
			Message: "chore: add " + sopsFilePath,
			Branch:  branch,
		}); err != nil {
			return fmt.Errorf("committing %s: %w", sopsFilePath, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading %s: %w", sopsFilePath, err)
	}

	current, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(existing.Content), ""))
	if err != nil {
		return fmt.Errorf("decoding %s: %w", sopsFilePath, err)
	}
	if string(current) == desired {
		return nil
	}
	if err := apiClient.UpdateFile(ctx, owner, repo.Name, sopsFilePath, forgejoapi.UpdateFileOptions{
		Content: encoded,
		Message: "chore: update " + sopsFilePath,
		Branch:  branch,
		SHA:     existing.SHA,
	}); err != nil {
		return fmt.Errorf("updating %s: %w", sopsFilePath, err)
	}
	return nil
}

// reconcileRemoteRepo creates the repository on the Forgejo instance if it
// does not exist yet, or otherwise updates the fields the API allows
// updating.
func reconcileRemoteRepo(ctx context.Context, apiClient *forgejoapi.Client, owner string, repo *forgejoschrankedev1alpha1.ForgejoRepository) error {
	repoName := repo.Name

	_, err := apiClient.GetRepo(ctx, owner, repoName)
	switch {
	case errors.Is(err, forgejoapi.ErrNotFound):
		_, err = apiClient.CreateOrgRepo(ctx, owner, forgejoapi.CreateRepoOptions{
			Name:          repoName,
			Description:   repo.Spec.Description,
			Private:       repo.Spec.Private,
			Template:      repo.Spec.Template,
			AutoInit:      repo.Spec.AutoInit,
			DefaultBranch: repo.Spec.DefaultBranch,
		})
		return err
	case err != nil:
		return err
	default:
		return apiClient.EditRepo(ctx, owner, repoName, forgejoapi.EditRepoOptions{
			Description:   repo.Spec.Description,
			Private:       repo.Spec.Private,
			Template:      repo.Spec.Template,
			DefaultBranch: repo.Spec.DefaultBranch,
		})
	}
}

// reconcileDelete removes the repository from the Forgejo instance and then
// releases the finalizer so the ForgejoRepository object itself can be
// garbage collected.
func (r *ForgejoRepositoryReconciler) reconcileDelete(ctx context.Context, repo *forgejoschrankedev1alpha1.ForgejoRepository) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(repo, repositoryFinalizer) {
		return ctrl.Result{}, nil
	}

	org, err := resolveOrganization(ctx, r.Client, organizationRefNamespace(repo.Spec.OrganizationRef, repo.Namespace), repo.Spec.OrganizationRef.Name)
	switch {
	case apierrors.IsNotFound(err):
		log.Info("ForgejoOrganization no longer exists, skipping remote repository cleanup",
			"organization", repo.Spec.OrganizationRef.Name)
	case err != nil:
		return ctrl.Result{}, err
	default:
		instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
		switch {
		case apierrors.IsNotFound(err):
			log.Info("Forgejo instance no longer exists, skipping remote repository cleanup",
				"instance", org.Spec.InstanceRef.Name)
		case err != nil:
			return ctrl.Result{}, err
		default:
			apiClient, err := buildAPIClient(ctx, r.Client, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
			if repo.Status.DeployKeyID != 0 {
				if err := apiClient.DeleteDeployKey(ctx, org.Name, repo.Name, repo.Status.DeployKeyID); err != nil {
					return ctrl.Result{}, err
				}
			}
			if err := apiClient.DeleteRepo(ctx, org.Name, repo.Name); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(repo, repositoryFinalizer)
	if err := r.Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ForgejoRepositoryReconciler) setStatus(ctx context.Context, repo *forgejoschrankedev1alpha1.ForgejoRepository, status, message string) (ctrl.Result, error) {
	repo.Status.Status = status
	repo.Status.Message = message
	if err := r.Status().Update(ctx, repo); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.ForgejoRepository{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Named("forgejorepository").
		Complete(r)
}
