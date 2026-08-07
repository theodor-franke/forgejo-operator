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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
)

// fluxReceiverGVK is the GroupVersionKind of a Flux notification-controller
// Receiver. It's fetched via an unstructured client rather than a typed one
// so this operator doesn't need to vendor the fluxcd notification-controller
// module just for this one lookup.
var fluxReceiverGVK = schema.GroupVersionKind{
	Group:   "notification.toolkit.fluxcd.io",
	Version: "v1",
	Kind:    "Receiver",
}

// fluxReceiverSecretTokenKey is the key Flux itself requires in the Secret a
// Receiver's spec.secretRef points at.
const fluxReceiverSecretTokenKey = "token"

// resolveReceiverWebhookURL fetches the Flux Receiver referenced by ref
// (always looked up by namespace and name, since a Receiver commonly lives
// in a different namespace than the ForgejoWebhook referencing it), reads
// its webhook path (status.webhookPath) and shared secret (the Secret named
// by spec.secretRef.name, in the Receiver's own namespace, key "token"), and
// combines the path with the target Forgejo instance's spec.base into the
// full URL Forgejo should deliver events to.
func resolveReceiverWebhookURL(
	ctx context.Context,
	c client.Client,
	ref forgejoschrankedev1alpha1.ForgejoWebhookReceiverRef,
	instance *forgejoschrankedev1alpha1.Forgejo,
) (webhookURL, secret string, err error) {
	receiver := &unstructured.Unstructured{}
	receiver.SetGroupVersionKind(fluxReceiverGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, receiver); err != nil {
		return "", "", fmt.Errorf("getting Receiver %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	webhookPath, found, err := unstructured.NestedString(receiver.Object, "status", "webhookPath")
	if err != nil {
		return "", "", fmt.Errorf("reading Receiver %s/%s status.webhookPath: %w", ref.Namespace, ref.Name, err)
	}
	if !found || webhookPath == "" {
		return "", "", fmt.Errorf("receiver %s/%s has no status.webhookPath yet", ref.Namespace, ref.Name)
	}

	secretName, found, err := unstructured.NestedString(receiver.Object, "spec", "secretRef", "name")
	if err != nil {
		return "", "", fmt.Errorf("reading Receiver %s/%s spec.secretRef.name: %w", ref.Namespace, ref.Name, err)
	}
	if !found || secretName == "" {
		return "", "", fmt.Errorf("receiver %s/%s has no spec.secretRef.name", ref.Namespace, ref.Name)
	}

	secret, err = fetchSecretKey(ctx, c, ref.Namespace, secretName, fluxReceiverSecretTokenKey)
	if err != nil {
		return "", "", err
	}

	if instance.Spec.Base == "" {
		return "", "", fmt.Errorf("forgejo instance %q has no spec.base configured, cannot build receiver webhook URL", instance.Name)
	}

	return strings.TrimRight(instance.Spec.Base, "/") + webhookPath, secret, nil
}
