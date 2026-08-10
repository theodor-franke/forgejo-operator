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

// organizationFinalizer is set on every ForgejoOrganization so the operator
// can delete the corresponding organization in Forgejo before the CR itself
// is removed from the cluster.
const organizationFinalizer = "forgejo.schranke.de/organization-finalizer"

// ForgejoOrganizationReconciler reconciles a ForgejoOrganization object
type ForgejoOrganizationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations/finalizers,verbs=update
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile creates (and keeps in sync) an organization and its teams on the
// referenced Forgejo instance for every ForgejoOrganization object, and
// pushes the resulting claimed-group-to-team mapping (aggregated across
// every ForgejoOrganization for that instance) to the instance's first
// OAuth2 authentication source.
func (r *ForgejoOrganizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	org := &forgejoschrankedev1alpha1.ForgejoOrganization{}
	if err := r.Get(ctx, req.NamespacedName, org); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !org.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, org)
	}

	if !controllerutil.ContainsFinalizer(org, organizationFinalizer) {
		controllerutil.AddFinalizer(org, organizationFinalizer)
		if err := r.Update(ctx, org); err != nil {
			return ctrl.Result{}, err
		}
	}

	instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve Forgejo instance", "instance", org.Spec.InstanceRef.Name)
		return r.setStatus(ctx, org, statusError, err.Error())
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, org, statusError, err.Error())
	}

	if err := reconcileRemoteOrg(ctx, apiClient, org); err != nil {
		log.Error(err, "unable to reconcile organization on Forgejo instance")
		return r.setStatus(ctx, org, statusError, err.Error())
	}

	if err := syncGroupTeamMap(ctx, r.Client, instance); err != nil {
		log.Error(err, "unable to sync group-team map on Forgejo instance")
		return r.setStatus(ctx, org, statusError, err.Error())
	}

	return r.setStatus(ctx, org, statusReady, "")
}

// ownersTeamName is Forgejo's built-in, non-deletable team; the reconciler
// never creates, edits, or deletes it even if a spec happens to name a team
// "Owners".
const ownersTeamName = "Owners"

const teamAccessModeAdmin = "admin"

// teamPermissionPlaceholder is sent as the non-admin "permission" value
// whenever a team's units_map is what actually carries the per-feature
// access; Forgejo requires a non-"admin" permission to accompany units_map
// at all (see the client.go gotcha note), but ignores the exact value.
const teamPermissionPlaceholder = "read"

// teamAccessArgs translates a Team's AccessMode/RepoPermissions into the
// (permission, unitsMap) pair forgejoapi's team CRUD calls expect.
//
// For AccessMode "admin" it returns ("admin", nil): Forgejo grants full
// access to every unit and ignores any units_map. Otherwise it returns a
// non-admin placeholder permission plus an explicit units_map built from
// RepoPermissions (defaulting unset fields to "none") — Forgejo only
// applies units_map when the request's permission is not "admin", so a
// team previously in admin mode would otherwise silently stay admin.
func teamAccessArgs(team forgejoschrankedev1alpha1.Team) (permission string, unitsMap map[string]string) {
	if team.AccessMode == teamAccessModeAdmin {
		return teamAccessModeAdmin, nil
	}
	return teamPermissionPlaceholder, map[string]string{
		forgejoapi.UnitCode:         accessLevel(team.RepoPermissions.Code),
		forgejoapi.UnitIssues:       accessLevel(team.RepoPermissions.Issues),
		forgejoapi.UnitPullRequests: accessLevel(team.RepoPermissions.PullRequests),
		forgejoapi.UnitReleases:     accessLevel(team.RepoPermissions.Releases),
		forgejoapi.UnitWiki:         accessLevel(team.RepoPermissions.Wiki),
		forgejoapi.UnitProjects:     accessLevel(team.RepoPermissions.Projects),
		forgejoapi.UnitPackages:     accessLevel(team.RepoPermissions.Packages),
		forgejoapi.UnitActions:      accessLevel(team.RepoPermissions.Actions),
	}
}

func accessLevel(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

// reconcileRemoteOrg creates the organization on the Forgejo instance if it
// does not exist yet (or otherwise updates the fields the API allows
// updating), then reconciles its teams to match org.Spec.Teams.
func reconcileRemoteOrg(ctx context.Context, apiClient *forgejoapi.Client, org *forgejoschrankedev1alpha1.ForgejoOrganization) error {
	orgName := org.Name

	_, err := apiClient.GetOrg(ctx, orgName)
	switch {
	case errors.Is(err, forgejoapi.ErrNotFound):
		if _, err := apiClient.CreateOrg(ctx, forgejoapi.CreateOrgOptions{
			Name:                      orgName,
			Visibility:                org.Spec.Visibility,
			RepoAdminChangeTeamAccess: org.Spec.AdminPermissionRights,
		}); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := apiClient.EditOrg(ctx, orgName, forgejoapi.EditOrgOptions{
			Visibility:                org.Spec.Visibility,
			RepoAdminChangeTeamAccess: org.Spec.AdminPermissionRights,
		}); err != nil {
			return err
		}
	}

	return reconcileTeams(ctx, apiClient, orgName, org.Spec.Teams)
}

// reconcileTeams creates/updates every team in desired and deletes any
// existing team that is no longer desired, leaving the built-in "Owners"
// team untouched either way.
func reconcileTeams(ctx context.Context, apiClient *forgejoapi.Client, orgName string, desired []forgejoschrankedev1alpha1.Team) error {
	existing, err := apiClient.ListTeams(ctx, orgName)
	if err != nil {
		return fmt.Errorf("listing teams: %w", err)
	}
	existingByName := make(map[string]forgejoapi.Team, len(existing))
	for _, t := range existing {
		existingByName[t.Name] = t
	}

	desiredNames := make(map[string]bool, len(desired))
	for _, team := range desired {
		desiredNames[team.Name] = true
		permission, unitsMap := teamAccessArgs(team)

		if existingTeam, ok := existingByName[team.Name]; ok {
			if err := apiClient.EditTeam(ctx, existingTeam.ID, forgejoapi.EditTeamOptions{
				Name:                    team.Name,
				Description:             team.Description,
				Permission:              permission,
				UnitsMap:                unitsMap,
				CanCreateOrgRepo:        team.CanCreateOrgRepo,
				IncludesAllRepositories: team.IncludesAllRepositories,
			}); err != nil {
				return fmt.Errorf("updating team %q: %w", team.Name, err)
			}
			continue
		}

		if _, err := apiClient.CreateTeam(ctx, orgName, forgejoapi.CreateTeamOptions{
			Name:                    team.Name,
			Description:             team.Description,
			Permission:              permission,
			UnitsMap:                unitsMap,
			CanCreateOrgRepo:        team.CanCreateOrgRepo,
			IncludesAllRepositories: team.IncludesAllRepositories,
		}); err != nil {
			return fmt.Errorf("creating team %q: %w", team.Name, err)
		}
	}

	for name, existingTeam := range existingByName {
		if name == ownersTeamName || desiredNames[name] {
			continue
		}
		if err := apiClient.DeleteTeam(ctx, existingTeam.ID); err != nil {
			return fmt.Errorf("deleting team %q: %w", name, err)
		}
	}

	return nil
}

// reconcileDelete removes the organization from the Forgejo instance and
// then releases the finalizer so the ForgejoOrganization object itself can
// be garbage collected.
func (r *ForgejoOrganizationReconciler) reconcileDelete(ctx context.Context, org *forgejoschrankedev1alpha1.ForgejoOrganization) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(org, organizationFinalizer) {
		return ctrl.Result{}, nil
	}

	instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
	switch {
	case apierrors.IsNotFound(err):
		log.Info("Forgejo instance no longer exists, skipping remote organization cleanup",
			"instance", org.Spec.InstanceRef.Name)
	case err != nil:
		return ctrl.Result{}, err
	default:
		apiClient, err := buildAPIClient(ctx, r.Client, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Deleting the organization cascades to its teams, so there is
		// nothing further to clean up on the Forgejo side for them.
		if err := apiClient.DeleteOrg(ctx, org.Name); err != nil {
			return ctrl.Result{}, err
		}
		// org still carries a non-zero DeletionTimestamp, so it is excluded
		// from this recomputation and its groups drop out of the map.
		if err := syncGroupTeamMap(ctx, r.Client, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("syncing group-team map after deletion: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(org, organizationFinalizer)
	if err := r.Update(ctx, org); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ForgejoOrganizationReconciler) setStatus(ctx context.Context, org *forgejoschrankedev1alpha1.ForgejoOrganization, status, message string) (ctrl.Result, error) {
	org.Status.Status = status
	org.Status.Message = message
	if err := r.Status().Update(ctx, org); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoOrganizationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.ForgejoOrganization{}).
		Named("forgejoorganization").
		Complete(r)
}
