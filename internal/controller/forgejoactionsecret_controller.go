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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

// actionSecretFinalizer is set on every ForgejoActionSecret so the operator
// can delete the corresponding Actions secret in Forgejo before the CR
// itself is removed from the cluster.
const actionSecretFinalizer = "forgejo.schranke.de/actionsecret-finalizer"

// ForgejoActionSecretReconciler reconciles a ForgejoActionSecret object
type ForgejoActionSecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoactionsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoactionsecrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoactionsecrets/finalizers,verbs=update
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile creates or updates a Forgejo Actions secret on the organization
// referenced by spec.organizationRef, using the value of a key in a
// Kubernetes Secret. Forgejo's Actions secrets are write-only (the API
// never returns a value, only its name), so there is no way to detect
// drift by reading the remote value back: every successful reconcile just
// re-applies it unconditionally, which is safe since the upsert is
// idempotent.
func (r *ForgejoActionSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	actionSecret := &forgejoschrankedev1alpha1.ForgejoActionSecret{}
	if err := r.Get(ctx, req.NamespacedName, actionSecret); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !actionSecret.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, actionSecret)
	}

	if !controllerutil.ContainsFinalizer(actionSecret, actionSecretFinalizer) {
		controllerutil.AddFinalizer(actionSecret, actionSecretFinalizer)
		if err := r.Update(ctx, actionSecret); err != nil {
			return ctrl.Result{}, err
		}
	}

	org, err := resolveOrganization(ctx, r.Client, organizationRefNamespace(actionSecret.Spec.OrganizationRef, actionSecret.Namespace), actionSecret.Spec.OrganizationRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve ForgejoOrganization", "organization", actionSecret.Spec.OrganizationRef.Name)
		return r.setStatus(ctx, actionSecret, statusError, err.Error())
	}

	instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
	if err != nil {
		log.Error(err, "unable to resolve Forgejo instance", "instance", org.Spec.InstanceRef.Name)
		return r.setStatus(ctx, actionSecret, statusError, err.Error())
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, actionSecret, statusError, err.Error())
	}

	valueFrom := actionSecret.Spec.ValueFrom
	value, err := fetchSecretKey(ctx, r.Client, valueFrom.SecretNamespace, valueFrom.SecretName, valueFrom.Key)
	if err != nil {
		log.Error(err, "unable to read action secret value")
		return r.setStatus(ctx, actionSecret, statusError, err.Error())
	}

	if err := apiClient.UpsertOrgActionSecret(ctx, org.Name, actionSecret.Spec.Name, value); err != nil {
		log.Error(err, "unable to upsert action secret on Forgejo instance")
		return r.setStatus(ctx, actionSecret, statusError, err.Error())
	}

	return r.setStatus(ctx, actionSecret, statusReady, "")
}

// reconcileDelete removes the Actions secret from the Forgejo instance and
// then releases the finalizer so the ForgejoActionSecret object itself can
// be garbage collected.
func (r *ForgejoActionSecretReconciler) reconcileDelete(ctx context.Context, actionSecret *forgejoschrankedev1alpha1.ForgejoActionSecret) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(actionSecret, actionSecretFinalizer) {
		return ctrl.Result{}, nil
	}

	org, err := resolveOrganization(ctx, r.Client, organizationRefNamespace(actionSecret.Spec.OrganizationRef, actionSecret.Namespace), actionSecret.Spec.OrganizationRef.Name)
	switch {
	case apierrors.IsNotFound(err):
		log.Info("ForgejoOrganization no longer exists, skipping remote action secret cleanup",
			"organization", actionSecret.Spec.OrganizationRef.Name)
	case err != nil:
		return ctrl.Result{}, err
	default:
		instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
		switch {
		case apierrors.IsNotFound(err):
			log.Info("Forgejo instance no longer exists, skipping remote action secret cleanup",
				"instance", org.Spec.InstanceRef.Name)
		case err != nil:
			return ctrl.Result{}, err
		default:
			apiClient, err := buildAPIClient(ctx, r.Client, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
			if err := apiClient.DeleteOrgActionSecret(ctx, org.Name, actionSecret.Spec.Name); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(actionSecret, actionSecretFinalizer)
	if err := r.Update(ctx, actionSecret); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ForgejoActionSecretReconciler) setStatus(ctx context.Context, actionSecret *forgejoschrankedev1alpha1.ForgejoActionSecret, status, message string) (ctrl.Result, error) {
	actionSecret.Status.Status = status
	actionSecret.Status.Message = message
	if err := r.Status().Update(ctx, actionSecret); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoActionSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.ForgejoActionSecret{}).
		Named("forgejoactionsecret").
		Complete(r)
}
