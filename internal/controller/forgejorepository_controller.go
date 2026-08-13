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
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

// repositoryFinalizer is set on every ForgejoRepository so the operator can
// delete the corresponding repository in Forgejo before the CR itself is
// removed from the cluster.
const repositoryFinalizer = "forgejo.schranke.de/repository-finalizer"

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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

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

	return r.setStatus(ctx, repo, statusReady, "")
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
		Named("forgejorepository").
		Complete(r)
}
