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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoweb"
)

// resolveInstance fetches the cluster-scoped Forgejo instance with the given name.
func resolveInstance(ctx context.Context, c client.Client, name string) (*forgejoschrankedev1alpha1.Forgejo, error) {
	instance := &forgejoschrankedev1alpha1.Forgejo{}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, instance); err != nil {
		return nil, err
	}
	return instance, nil
}

// resolveOrganization fetches the ForgejoOrganization with the given
// namespace and name.
func resolveOrganization(ctx context.Context, c client.Client, namespace, name string) (*forgejoschrankedev1alpha1.ForgejoOrganization, error) {
	org := &forgejoschrankedev1alpha1.ForgejoOrganization{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, org); err != nil {
		return nil, err
	}
	return org, nil
}

// organizationRefNamespace returns the namespace an OrganizationRef should
// be resolved in: the explicit namespace if set, otherwise objNamespace (the
// namespace of the object doing the referencing).
func organizationRefNamespace(ref forgejoschrankedev1alpha1.OrganizationRef, objNamespace string) string {
	if ref.Namespace != "" {
		return ref.Namespace
	}
	return objNamespace
}

// resolveRepository fetches the ForgejoRepository with the given namespace
// and name.
func resolveRepository(ctx context.Context, c client.Client, namespace, name string) (*forgejoschrankedev1alpha1.ForgejoRepository, error) {
	repo := &forgejoschrankedev1alpha1.ForgejoRepository{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, repo); err != nil {
		return nil, err
	}
	return repo, nil
}

// fetchSecretKey loads a single key's value out of a namespaced Kubernetes Secret.
func fetchSecretKey(ctx context.Context, c client.Client, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret); err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}
	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s missing key %q", namespace, name, key)
	}
	return string(value), nil
}

// fetchCredentials loads the username/password referenced by
// instance.Spec.AuthSecretRef.
func fetchCredentials(ctx context.Context, c client.Client, instance *forgejoschrankedev1alpha1.Forgejo) (username, password string, err error) {
	secretRef := instance.Spec.AuthSecretRef

	username, err = fetchSecretKey(ctx, c, secretRef.SecretNamespace, secretRef.SecretName, secretRef.UsernameKey)
	if err != nil {
		return "", "", err
	}
	password, err = fetchSecretKey(ctx, c, secretRef.SecretNamespace, secretRef.SecretName, secretRef.PasswordKey)
	if err != nil {
		return "", "", err
	}
	return username, password, nil
}

// buildAPIClient loads the credentials referenced by instance.Spec.AuthSecretRef
// and returns a Forgejo REST API client for that instance.
func buildAPIClient(ctx context.Context, c client.Client, instance *forgejoschrankedev1alpha1.Forgejo) (*forgejoapi.Client, error) {
	username, password, err := fetchCredentials(ctx, c, instance)
	if err != nil {
		return nil, err
	}
	return forgejoapi.New(instance.Spec.Url, username, password), nil
}

// buildWebClient loads the credentials referenced by instance.Spec.AuthSecretRef
// and returns a Forgejo web-admin client for that instance, for the handful
// of settings (like the group-team map) that have no REST API.
func buildWebClient(ctx context.Context, c client.Client, instance *forgejoschrankedev1alpha1.Forgejo) (*forgejoweb.Client, error) {
	username, password, err := fetchCredentials(ctx, c, instance)
	if err != nil {
		return nil, err
	}
	return forgejoweb.New(instance.Spec.Url, username, password)
}
