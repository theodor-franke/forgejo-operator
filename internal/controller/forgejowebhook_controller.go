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
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
)

// webhookFinalizer is set on every ForgejoWebhook so the operator can delete
// the corresponding webhook in Forgejo before the CR itself is removed.
const webhookFinalizer = "forgejo.schranke.de/webhook-finalizer"

// defaultHookType is the Forgejo webhook payload type used unless
// spec.type overrides it: Forgejo is a Gitea fork and shares its webhook
// signature format, which is what Flux's "gitea" Receiver type expects.
const defaultHookType = "forgejo"

// defaultHookEvent is the event ForgejoWebhook registers for when
// spec.events is left unset.
const defaultHookEvent = "push"

// defaultBranchFilter is the branch filter Forgejo applies when
// spec.branchFilter is left unset.
const defaultBranchFilter = "*"

// ForgejoWebhookReconciler reconciles a ForgejoWebhook object
type ForgejoWebhookReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejowebhooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejowebhooks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejowebhooks/finalizers,verbs=update
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejorepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoorganizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgejo.schranke.de,resources=forgejoes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=notification.toolkit.fluxcd.io,resources=receivers,verbs=get;list;watch

// Reconcile creates (and keeps in sync) a webhook on the repository
// referenced by spec.repositoryRef or spec.repository, delivering events to
// the Flux Receiver referenced by spec.receiverRef.
func (r *ForgejoWebhookReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	webhook := &forgejoschrankedev1alpha1.ForgejoWebhook{}
	if err := r.Get(ctx, req.NamespacedName, webhook); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !webhook.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, webhook)
	}

	if !controllerutil.ContainsFinalizer(webhook, webhookFinalizer) {
		controllerutil.AddFinalizer(webhook, webhookFinalizer)
		if err := r.Update(ctx, webhook); err != nil {
			return ctrl.Result{}, err
		}
	}

	owner, repoName, instance, err := r.resolveTarget(ctx, webhook)
	if err != nil {
		log.Error(err, "unable to resolve target repository")
		return r.setStatus(ctx, webhook, statusError, err.Error())
	}

	apiClient, err := buildAPIClient(ctx, r.Client, instance)
	if err != nil {
		log.Error(err, "unable to build Forgejo API client")
		return r.setStatus(ctx, webhook, statusError, err.Error())
	}

	webhookURL, secret, err := resolveReceiverWebhookURL(ctx, r.Client, webhook.Spec.ReceiverRef, instance)
	if err != nil {
		log.Error(err, "unable to resolve Receiver webhook URL")
		return r.setStatus(ctx, webhook, statusError, err.Error())
	}

	hookID, err := reconcileRemoteHook(ctx, apiClient, owner, repoName, webhook, webhookURL, secret)
	if err != nil {
		log.Error(err, "unable to reconcile webhook on Forgejo instance")
		return r.setStatus(ctx, webhook, statusError, err.Error())
	}

	webhook.Status.URL = webhookURL
	webhook.Status.HookID = hookID
	return r.setStatus(ctx, webhook, statusReady, "")
}

// resolveTarget resolves the repository owner/name and Forgejo instance a
// ForgejoWebhook targets, following whichever of spec.repositoryRef or
// spec.repository is set.
func (r *ForgejoWebhookReconciler) resolveTarget(ctx context.Context, webhook *forgejoschrankedev1alpha1.ForgejoWebhook) (owner, repoName string, instance *forgejoschrankedev1alpha1.Forgejo, err error) {
	switch {
	case webhook.Spec.RepositoryRef != nil:
		repo, err := resolveRepository(ctx, r.Client, webhook.Namespace, webhook.Spec.RepositoryRef.Name)
		if err != nil {
			return "", "", nil, fmt.Errorf("resolving ForgejoRepository %s/%s: %w", webhook.Namespace, webhook.Spec.RepositoryRef.Name, err)
		}
		orgNamespace := organizationRefNamespace(repo.Spec.OrganizationRef, repo.Namespace)
		org, err := resolveOrganization(ctx, r.Client, orgNamespace, repo.Spec.OrganizationRef.Name)
		if err != nil {
			return "", "", nil, fmt.Errorf("resolving ForgejoOrganization %s/%s: %w", orgNamespace, repo.Spec.OrganizationRef.Name, err)
		}
		instance, err := resolveInstance(ctx, r.Client, org.Spec.InstanceRef.Name)
		if err != nil {
			return "", "", nil, fmt.Errorf("resolving Forgejo instance %q: %w", org.Spec.InstanceRef.Name, err)
		}
		return org.Name, repo.Name, instance, nil

	case webhook.Spec.Repository != nil:
		instance, err := resolveInstance(ctx, r.Client, webhook.Spec.Repository.InstanceRef.Name)
		if err != nil {
			return "", "", nil, fmt.Errorf("resolving Forgejo instance %q: %w", webhook.Spec.Repository.InstanceRef.Name, err)
		}
		owner, repoName, err := parseRepositoryURL(webhook.Spec.Repository.URL)
		if err != nil {
			return "", "", nil, err
		}
		return owner, repoName, instance, nil

	default:
		return "", "", nil, fmt.Errorf("exactly one of spec.repositoryRef or spec.repository must be set")
	}
}

// parseRepositoryURL extracts the owner and repository name from a
// repository's web URL, e.g. https://forgejo.example.com/my-org/my-repo(.git).
func parseRepositoryURL(rawURL string) (owner, repo string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parsing repository URL %q: %w", rawURL, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return "", "", fmt.Errorf("repository URL %q must have the form https://host/owner/repo", rawURL)
	}
	return segments[0], strings.TrimSuffix(segments[1], ".git"), nil
}

// hookConfigURLKey is the config map key Forgejo uses for a webhook's
// target URL, matched against to find a webhook this ForgejoWebhook already
// created on an earlier reconcile.
const hookConfigURLKey = "url"

// reconcileRemoteHook creates the webhook on the Forgejo repository if it
// does not exist yet (matched by target URL, since a ForgejoWebhook has a
// stable one-to-one relationship with its target URL), or otherwise updates
// it to match spec.
func reconcileRemoteHook(
	ctx context.Context,
	apiClient *forgejoapi.Client,
	owner, repoName string,
	webhook *forgejoschrankedev1alpha1.ForgejoWebhook,
	webhookURL, secret string,
) (int64, error) {
	hookType := webhook.Spec.Type
	if hookType == "" {
		hookType = defaultHookType
	}
	events := webhook.Spec.Events
	if len(events) == 0 {
		events = []string{defaultHookEvent}
	}
	branchFilter := webhook.Spec.BranchFilter
	if branchFilter == "" {
		branchFilter = defaultBranchFilter
	}
	config := map[string]string{
		hookConfigURLKey: webhookURL,
		"content_type":   "json",
		"secret":         secret,
	}
	active := !webhook.Spec.Suspend

	hooks, err := apiClient.ListRepoHooks(ctx, owner, repoName)
	if err != nil {
		return 0, err
	}
	for _, hook := range hooks {
		if hook.Config[hookConfigURLKey] != webhookURL {
			continue
		}
		err := apiClient.EditRepoHook(ctx, owner, repoName, hook.ID, forgejoapi.EditHookOptions{
			Config:       config,
			Events:       events,
			Active:       active,
			BranchFilter: branchFilter,
		})
		return hook.ID, err
	}

	created, err := apiClient.CreateRepoHook(ctx, owner, repoName, forgejoapi.CreateHookOptions{
		Type:         hookType,
		Config:       config,
		Events:       events,
		Active:       active,
		BranchFilter: branchFilter,
	})
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// reconcileDelete removes the webhook from the Forgejo repository and then
// releases the finalizer so the ForgejoWebhook object itself can be garbage
// collected.
func (r *ForgejoWebhookReconciler) reconcileDelete(ctx context.Context, webhook *forgejoschrankedev1alpha1.ForgejoWebhook) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(webhook, webhookFinalizer) {
		return ctrl.Result{}, nil
	}

	owner, repoName, instance, err := r.resolveTarget(ctx, webhook)
	switch {
	case err != nil && apierrors.IsNotFound(err):
		log.Info("target repository or instance no longer exists, skipping remote webhook cleanup")
	case err != nil:
		// A non-NotFound resolution error (e.g. a malformed spec.repository.url)
		// would otherwise permanently block deletion, so log and proceed to
		// drop the finalizer rather than getting the object stuck.
		log.Error(err, "unable to resolve target repository for webhook cleanup, skipping remote cleanup")
	default:
		apiClient, err := buildAPIClient(ctx, r.Client, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		hooks, err := apiClient.ListRepoHooks(ctx, owner, repoName)
		switch {
		case errors.Is(err, forgejoapi.ErrNotFound):
			log.Info("target repository no longer exists on Forgejo, skipping remote webhook cleanup")
			hooks = nil
		case err != nil:
			return ctrl.Result{}, err
		}
		for _, hook := range hooks {
			if hook.Config[hookConfigURLKey] != webhook.Status.URL {
				continue
			}
			if err := apiClient.DeleteRepoHook(ctx, owner, repoName, hook.ID); err != nil {
				return ctrl.Result{}, err
			}
			break
		}
	}

	controllerutil.RemoveFinalizer(webhook, webhookFinalizer)
	if err := r.Update(ctx, webhook); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ForgejoWebhookReconciler) setStatus(ctx context.Context, webhook *forgejoschrankedev1alpha1.ForgejoWebhook, status, message string) (ctrl.Result, error) {
	webhook.Status.Status = status
	webhook.Status.Message = message
	if err := r.Status().Update(ctx, webhook); err != nil {
		return ctrl.Result{}, err
	}
	if status != statusReady {
		return ctrl.Result{}, fmt.Errorf("%s", message)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ForgejoWebhookReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgejoschrankedev1alpha1.ForgejoWebhook{}).
		Named("forgejowebhook").
		Complete(r)
}
