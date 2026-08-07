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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

// forgejoReadyRequeueInterval controls how often a healthy Forgejo instance is
// re-polled for its version, to notice upgrades of the underlying instance.
const forgejoReadyRequeueInterval = 5 * time.Minute

// ForgejoReconciler reconciles a Forgejo object
type ForgejoReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create

// Reconcile connects to the Forgejo instance described by the Forgejo object
// (an already-running Forgejo, not one this operator deploys) and records its
// reported version in status.
func (r *ForgejoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	instance := &forgejoschrankedev1alpha1.Forgejo{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, instance, statusError, "", err.Error())
	}

	version, err := apiClient.GetVersion(ctx)
	if err != nil {
		log.Error(err, "unable to fetch Forgejo version")
		return r.setStatus(ctx, instance, statusError, "", err.Error())
	}

	if err := ensureDefaultPullKeySecret(ctx, r.Client, instance); err != nil {
		log.Error(err, "unable to ensure default pull key secret")
		return r.setStatus(ctx, instance, statusError, version, err.Error())
	}

	return r.setStatus(ctx, instance, statusReady, version, "")
}

func (r *ForgejoReconciler) setStatus(ctx context.Context, instance *forgejoschrankedev1alpha1.Forgejo, status, version, message string) (ctrl.Result, error) {
	instance.Status.Status = status
	instance.Status.Version = version
	instance.Status.Message = message
	if err := r.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{RequeueAfter: forgejoReadyRequeueInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.Forgejo{}).
		Named("forgejo").
		Complete(r)
}
