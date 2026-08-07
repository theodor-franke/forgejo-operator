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
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	"github.com/theodor-franke/forgejo-operator/internal/forgejoweb"
)

// computeGroupTeamMap aggregates, across every ForgejoOrganization that
// targets the given Forgejo instance, each team's claimed Groups into
// Forgejo's "map claimed groups to organization teams" shape:
//
//	{"<group>": {"<organization>": ["<team>", ...]}}
//
// Organizations that are themselves being deleted are excluded, so a
// group's mapping shrinks as soon as its owning ForgejoOrganization starts
// deletion rather than only once the finalizer finishes.
func computeGroupTeamMap(ctx context.Context, c client.Client, instanceName string) (map[string]map[string][]string, error) {
	list := &forgejoschrankedev1alpha1.ForgejoOrganizationList{}
	if err := c.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing ForgejoOrganizations: %w", err)
	}

	result := map[string]map[string][]string{}
	for _, org := range list.Items {
		if org.Spec.InstanceRef.Name != instanceName || !org.DeletionTimestamp.IsZero() {
			continue
		}
		for _, team := range org.Spec.Teams {
			for _, group := range team.Groups {
				if result[group] == nil {
					result[group] = map[string][]string{}
				}
				result[group][org.Name] = append(result[group][org.Name], team.Name)
			}
		}
	}
	return result, nil
}

// syncGroupTeamMap recomputes the group-team map for instance and pushes it
// to the first configured OAuth2 authentication source. It is a no-op, not
// an error, when the instance has no OAuth2 authentication source
// configured yet.
func syncGroupTeamMap(ctx context.Context, c client.Client, instance *forgejoschrankedev1alpha1.Forgejo) error {
	computed, err := computeGroupTeamMap(ctx, c, instance.Name)
	if err != nil {
		return err
	}

	webClient, err := buildWebClient(ctx, c, instance)
	if err != nil {
		return fmt.Errorf("building Forgejo web client: %w", err)
	}

	sourceID, err := webClient.FirstOAuth2AuthSourceID(ctx)
	if errors.Is(err, forgejoweb.ErrNoOAuth2Source) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finding an OAuth2 authentication source: %w", err)
	}

	payload, err := json.Marshal(computed)
	if err != nil {
		return fmt.Errorf("encoding group-team map: %w", err)
	}

	if err := webClient.SetGroupTeamMap(ctx, sourceID, string(payload)); err != nil {
		return fmt.Errorf("updating group-team map on authentication source %d: %w", sourceID, err)
	}
	return nil
}
