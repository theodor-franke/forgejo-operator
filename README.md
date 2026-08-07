# forgejo-operator

A Kubernetes operator that **configures an already-running [Forgejo](https://forgejo.org/) instance** — it does not deploy or manage the Forgejo server itself.

## Description

forgejo-operator lets you manage Forgejo organizations, teams, repositories, Actions secrets, and repository webhooks declaratively as Kubernetes custom resources, talking to Forgejo's real REST API (and, for the one setting with no API, its web admin UI) instead of clicking through the Forgejo UI by hand. It has five CRDs, all in group `forgejo.schranke.de/v1alpha1`:

- **`Forgejo`** (cluster-scoped) — points at an existing Forgejo server (URL + credentials) and records its version in status. Can optionally manage a shared SSH deploy-key pair handed out to every repository on the instance, and a base URL used by `ForgejoWebhook` (below).
- **`ForgejoOrganization`** — creates/updates an organization and its teams (with per-feature Forgejo-style permissions), and maps each team's IdP-claimed groups into the instance's OAuth2 "map claimed groups to organization teams" setting.
- **`ForgejoActionSecret`** — upserts a Forgejo Actions secret on an organization, sourced from a Kubernetes `Secret`.
- **`ForgejoRepository`** — creates/updates a repository under an organization, optionally adding the instance's shared default deploy key.
- **`ForgejoWebhook`** — creates a repository webhook that delivers events to a [Flux](https://fluxcd.io/) `Receiver`, integrating this operator with Flux's push-to-reconcile flow.

See [`CLAUDE.md`](./CLAUDE.md) for a detailed architecture walkthrough, and `config/samples/` for example manifests of every CRD.

## Getting Started

### Prerequisites
- go version v1.25.0+
- docker version 17.03+ (also required to run the test suite — see below)
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster
- [cert-manager](https://cert-manager.io/), already installed in the target cluster (required for the validating webhook's TLS certificate — see [`CLAUDE.md`](./CLAUDE.md#distribution-helm-chart-and-ci))

### To Deploy on the cluster

Two independent deployment paths are supported — see [`CLAUDE.md`](./CLAUDE.md#deployment-against-a-realkind-cluster) for the full rundown of both, including their respective `make` targets:

- **Kustomize**: `make install` (CRDs) → `make deploy IMG=<registry>/forgejo-operator:tag` (manager).
- **Helm**: `make helm-deploy IMG=<registry>/forgejo-operator:tag`, installing `dist/chart` (a hand-written chart — see [`CLAUDE.md`](./CLAUDE.md#distribution-helm-chart-and-ci)).

Once deployed, create a `Forgejo` resource pointing at your instance's URL and credentials, then layer `ForgejoOrganization`/`ForgejoRepository`/`ForgejoActionSecret`/`ForgejoWebhook` resources on top — see `config/samples/` for a worked example of each:

```sh
kubectl apply -k config/samples/
```

### To Uninstall

```sh
kubectl delete -k config/samples/   # remove the CRs first
make uninstall                      # remove the CRDs
make undeploy                       # remove the manager (kustomize path)
# or: make helm-uninstall           # remove the manager (Helm path)
```

## Development

Run `make help` for the full list of available `make` targets. The most commonly needed ones:

- `make build` — build the manager binary (runs `manifests generate fmt vet` first).
- `make run` — run the controller locally against the current kubeconfig context.
- `make test` — run the full unit/integration test suite. **Requires Docker**: the controller tests boot a real Forgejo server via testcontainers-go rather than mocking it, so there's no separate e2e suite.
- `make manifests` / `make generate` — regenerate CRD manifests/RBAC and DeepCopy code from kubebuilder markers after editing a type in `api/v1alpha1/`.
- `make lint` / `make lint-fix` — golangci-lint.

See [`CLAUDE.md`](./CLAUDE.md) for the full architecture and testing writeup, including how each CRD's reconciler is structured and what's verified against a real Forgejo server versus Kubernetes-side only.

## Contributing

Issues and pull requests are welcome. Before opening a PR, please make sure `make test` and `make lint` pass locally (both require Docker).

More information on the underlying scaffolding can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html).

## License

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
