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
	"net/url"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoapi"
	"github.com/theodor-franke/forgejo-operator/internal/sshkey"
)

const (
	// Keys used in the Secret referenced by Forgejo.Spec.DefaultPullKeySecretRef.
	pullKeySecretPublicKeyField  = "publicKey"
	pullKeySecretPrivateKeyField = "privateKey"
	pullKeySecretKnownHostsField = "knownHosts"

	// defaultSSHPort is used when Forgejo.Spec.SSHPort is unset.
	defaultSSHPort = 22

	// defaultPullKeyTitle identifies the default pull key's deploy key on
	// every repository it's added to, so it can be found again idempotently.
	defaultPullKeyTitle = "forgejo-operator-default-pull-key"
)

// ensureDefaultPullKeySecret makes sure the Secret referenced by
// instance.Spec.DefaultPullKeySecretRef exists, generating a new ed25519
// deploy-key pair and discovering the instance's SSH host key if it
// doesn't. An already-existing Secret is left untouched.
func ensureDefaultPullKeySecret(ctx context.Context, c client.Client, instance *forgejoschrankedev1alpha1.Forgejo) error {
	ref := instance.Spec.DefaultPullKeySecretRef
	if ref == nil {
		return nil
	}

	key := client.ObjectKey{Name: ref.SecretName, Namespace: ref.SecretNamespace}
	err := c.Get(ctx, key, &corev1.Secret{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting default pull key secret %s/%s: %w", ref.SecretNamespace, ref.SecretName, err)
	}

	host, port, err := sshHostPort(instance)
	if err != nil {
		return err
	}

	pair, err := sshkey.Generate(fmt.Sprintf("%s@%s", defaultPullKeyTitle, instance.Name))
	if err != nil {
		return fmt.Errorf("generating default pull key: %w", err)
	}

	knownHosts, err := sshkey.ScanHostKey(ctx, host, port)
	if err != nil {
		return fmt.Errorf("discovering SSH host key for %s:%d: %w", host, port, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ref.SecretName, Namespace: ref.SecretNamespace},
		StringData: map[string]string{
			pullKeySecretPublicKeyField:  pair.PublicKey,
			pullKeySecretPrivateKeyField: pair.PrivateKey,
			pullKeySecretKnownHostsField: knownHosts,
		},
	}
	if err := c.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating default pull key secret %s/%s: %w", ref.SecretNamespace, ref.SecretName, err)
	}
	return nil
}

// sshHostPort resolves the SSH host/port to scan for a host key, applying
// Forgejo's documented defaults: the hostname portion of Spec.Url, and port 22.
func sshHostPort(instance *forgejoschrankedev1alpha1.Forgejo) (string, int32, error) {
	host := instance.Spec.SSHHost
	if host == "" {
		parsed, err := url.Parse(instance.Spec.Url)
		if err != nil {
			return "", 0, fmt.Errorf("parsing spec.url %q to derive the default SSH host: %w", instance.Spec.Url, err)
		}
		host = parsed.Hostname()
		if host == "" {
			return "", 0, fmt.Errorf("spec.url %q has no hostname to derive the default SSH host from; set spec.sshHost explicitly", instance.Spec.Url)
		}
	}

	port := instance.Spec.SSHPort
	if port == 0 {
		port = defaultSSHPort
	}
	return host, port, nil
}

// reconcileDefaultPullKey ensures repo has (or, if it opts out, does not
// have) a deploy key for instance's shared default pull key. It is a no-op
// if the instance has no DefaultPullKeySecretRef configured.
func reconcileDefaultPullKey(ctx context.Context, c client.Client, apiClient *forgejoapi.Client, instance *forgejoschrankedev1alpha1.Forgejo, owner, repoName string, optOut bool) error {
	ref := instance.Spec.DefaultPullKeySecretRef
	if ref == nil {
		return nil
	}

	keys, err := apiClient.ListDeployKeys(ctx, owner, repoName)
	if err != nil {
		return fmt.Errorf("listing deploy keys: %w", err)
	}
	var existing *forgejoapi.DeployKey
	for i := range keys {
		if keys[i].Title == defaultPullKeyTitle {
			existing = &keys[i]
			break
		}
	}

	if optOut {
		if existing == nil {
			return nil
		}
		if err := apiClient.DeleteDeployKey(ctx, owner, repoName, existing.ID); err != nil {
			return fmt.Errorf("removing opted-out default pull key: %w", err)
		}
		return nil
	}

	if existing != nil {
		return nil
	}

	publicKey, err := fetchSecretKey(ctx, c, ref.SecretNamespace, ref.SecretName, pullKeySecretPublicKeyField)
	if err != nil {
		return fmt.Errorf("reading default pull key: %w", err)
	}
	if _, err := apiClient.CreateDeployKey(ctx, owner, repoName, forgejoapi.CreateDeployKeyOptions{
		Title:    defaultPullKeyTitle,
		Key:      publicKey,
		ReadOnly: true,
	}); err != nil {
		return fmt.Errorf("adding default pull key: %w", err)
	}
	return nil
}
