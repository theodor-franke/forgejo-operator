# Implementation spec: `spec.sops` on `ForgejoRepository`

## Context

`cluster-projects` (sibling repo) currently provisions each project's SOPS
age keypair via a Helm pre-install/pre-upgrade hook Job
(`templates/sops-age-key.yaml`) that runs inside the project's own
Kubernetes namespace. That namespace is created by an *ordinary* (non-hook)
template, which only applies after all hooks finish — so a brand-new
project with `kustomize.sops: true` from day one fails on first install
(`namespaces "X" not found`), because the hook runs before its own
namespace exists. Working around that inside the Helm chart requires an
awkward secondary namespace-bootstrap hook running in the release namespace
(already implemented as a stopgap) that only exists to paper over Helm hook
ordering — it doesn't belong in a Helm chart at all.

`ForgejoRepository` is a better owner for this: it's already reconciled by
a real controller, it's already created in the *same* Kubernetes namespace
as the project workloads (see `cluster-projects/templates/repository.yaml`
— `ForgejoRepository` and the project's `GitRepository`/namespace all share
`{org}-{project}`), and it already has a near-identical feature
(`spec.deployKey`) to model this on: generate a keypair, publish half of it
as a cluster Secret, and the other half to Forgejo itself. SOPS is the same
shape, except the "other half" goes to Forgejo as a *committed file*
(`.sops.yaml`) instead of a deploy key API call, and there's a second
cluster-side artifact (a ConfigMap with the public key, so project owners
can fetch it without read access to the Secret).

This spec covers **only this repo** (`forgejo-operator`). It does not touch
`cluster-projects` — see "Follow-up in cluster-projects" at the end for
what has to change there once this ships, but that's separate work.

## Goal

Add `spec.sops` to `ForgejoRepository`. When `spec.sops.enabled: true`:

1. Generate an age keypair (once; never rotate an existing one) if one
   doesn't already exist.
2. Publish the private half as a Secret in the `ForgejoRepository`'s own
   namespace (same namespace Flux's `Kustomization.spec.decryption.secretRef`
   in that namespace will reference).
3. Publish the public half as a ConfigMap in the same namespace, so project
   owners can fetch it to encrypt secrets without needing read access to
   the Secret.
4. Commit a `.sops.yaml` file (containing that public key) to the
   repository's default branch on Forgejo, once, right after the Secret is
   first created.

All of this must be idempotent and safe to re-run on every reconcile, the
same way `spec.deployKey` is: check-before-create, no destructive
regeneration.

## 1. API changes — `api/v1alpha1/forgejorepository_types.go`

Add a `Sops` field to `ForgejoRepositorySpec`, modeled directly on
`DeployKeySpec`/`DeployKey` immediately above it in that file:

```go
// Sops provisions a SOPS age keypair for this repository: the private key
// is published as a Secret in this repository's own namespace (for use as
// a Flux Kustomization's spec.decryption.secretRef), the public key as a
// ConfigMap in the same namespace, and a .sops.yaml file referencing the
// public key is committed to the repository's default branch. Defaults to
// disabled for backward compat.
// +optional
Sops *SopsSpec `json:"sops,omitempty"`
```

```go
// SopsSpec configures automatic provisioning of a SOPS age keypair for
// decrypting secrets in this repository, published as a Secret +
// ConfigMap pair in the referencing ForgejoRepository's own namespace, with
// the public key also committed to the repository as .sops.yaml so project
// owners can encrypt new secrets without needing read access to the Secret.
type SopsSpec struct {
	// Enabled provisions the age keypair, Secret, ConfigMap, and committed
	// .sops.yaml when true.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretName overrides the generated Secret's name. Defaults to
	// "sops-age" when unset — this matches the fixed name
	// cluster-projects' Flux Kustomizations already reference as
	// spec.decryption.secretRef.name, so leave this unset unless you know
	// you need to change it.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// PublicKeyConfigMapName overrides the generated ConfigMap's name.
	// Defaults to "sops-age-public-key" when unset.
	// +optional
	PublicKeyConfigMapName string `json:"publicKeyConfigMapName,omitempty"`
}
```

Add matching status fields to `ForgejoRepositoryStatus`, following exactly
the reasoning already given for `DeployKeySecretName` (so disable/removal
can still find and clean up the right objects even if the name fields
change after the fact):

```go
// SopsSecretName is the name of the Secret spec.sops last published the
// age keypair as, so it can still be found and removed if
// spec.sops.secretName changes or spec.sops is removed entirely.
// +optional
SopsSecretName string `json:"sopsSecretName,omitempty"`

// SopsPublicKeyConfigMapName is the name of the ConfigMap spec.sops last
// published the public key as, so it can still be found and removed if
// spec.sops.publicKeyConfigMapName changes or spec.sops is removed
// entirely.
// +optional
SopsPublicKeyConfigMapName string `json:"sopsPublicKeyConfigMapName,omitempty"`
```

No webhook/CEL validation needed — there's nothing mutually exclusive here.

After editing this file: `make generate manifests` (regenerates
`zz_generated.deepcopy.go` and `config/crd/bases/*.yaml`). Also resync
`dist/chart/templates/crds/forgejo.schranke.de_forgejorepositories.yaml`
from the regenerated base per the existing manual-resync note in
`CLAUDE.md`'s Distribution section.

## 2. New package — `internal/agekey/agekey.go`

Mirror `internal/sshkey/sshkey.go`'s shape exactly (doc comment, package
shape, no shelling out). Use `filippo.io/age` (`go get filippo.io/age`,
run `go mod tidy` after) — it's the reference pure-Go age implementation,
same spirit as using `golang.org/x/crypto/ssh` instead of shelling out to
`ssh-keygen`.

```go
// Package agekey generates SOPS-compatible age keypairs, using
// filippo.io/age (no shelling out to age-keygen).
package agekey

import (
	"fmt"
	"time"

	"filippo.io/age"
)

// KeyPair is an age keypair in the text formats sops/age-keygen expect.
type KeyPair struct {
	// PublicKey is the age1... recipient string.
	PublicKey string
	// PrivateKey is the age-keygen -o file text format: a "# created:"
	// comment, a "# public key:" comment, then the AGE-SECRET-KEY-1...
	// identity line. sops/Flux's age decryption only looks for the
	// AGE-SECRET-KEY-1 line; the comments are for human readability when
	// inspecting the Secret directly, matching what age-keygen itself
	// produces.
	PrivateKey string
}

// Generate creates a new age X25519 keypair.
func Generate() (*KeyPair, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("agekey: generating X25519 identity: %w", err)
	}

	public := id.Recipient().String()
	private := fmt.Sprintf(
		"# created: %s\n# public key: %s\n%s\n",
		time.Now().Format(time.RFC3339), public, id.String(),
	)

	return &KeyPair{PublicKey: public, PrivateKey: private}, nil
}
```

(Verify the exact `filippo.io/age` public API with `go doc filippo.io/age`
once it's added as a dependency — `GenerateX25519Identity`,
`(*X25519Identity).String`, `(*X25519Identity).Recipient`,
`(*X25519Recipient).String` are the expected names/shapes as of the
current stable release, but confirm against whatever version `go get`
actually resolves.)

## 3. Forgejo Contents API — `internal/forgejoapi/client.go`

Add typed methods for Gitea/Forgejo's repository Contents API (same
dependency-free style as every other method in this file — see
`CreateDeployKey`/`ListDeployKeys` immediately above `ListRepoHooks` for
the pattern to match). Needed: read a file (to get its current `sha` for
idempotent updates) and create/update a file.

```go
// ContentsResponse is the subset of Forgejo's repository-contents response
// this operator cares about.
type ContentsResponse struct {
	SHA     string `json:"sha"`
	Content string `json:"content"` // base64, per the API
}

// GetContents fetches filepath at ref (branch/tag/commit) in owner/repo.
// Returns ErrNotFound if the file doesn't exist at that ref.
func (c *Client) GetContents(ctx context.Context, owner, repo, filepath, ref string) (*ContentsResponse, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), filepath, url.QueryEscape(ref))
	var out ContentsResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateFileOptions is the request body for CreateFile.
type CreateFileOptions struct {
	Content string `json:"content"` // base64
	Message string `json:"message"`
	Branch  string `json:"branch"`
}

// CreateFile creates filepath in owner/repo on branch. Fails if the file
// already exists — callers must GetContents first to decide create vs.
// update, same idiom as deploy keys/webhooks elsewhere in this client.
func (c *Client) CreateFile(ctx context.Context, owner, repo, filepath string, opts CreateFileOptions) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", url.PathEscape(owner), url.PathEscape(repo), filepath)
	return c.do(ctx, http.MethodPost, path, opts, nil)
}

// UpdateFileOptions is the request body for UpdateFile.
type UpdateFileOptions struct {
	Content string `json:"content"` // base64
	Message string `json:"message"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha"` // the file's current blob SHA, from GetContents
}

// UpdateFile updates the already-existing filepath in owner/repo on branch.
func (c *Client) UpdateFile(ctx context.Context, owner, repo, filepath string, opts UpdateFileOptions) error {
	path := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", url.PathEscape(owner), url.PathEscape(repo), filepath)
	return c.do(ctx, http.MethodPut, path, opts, nil)
}
```

`filepath` passed by callers here will always be the literal `.sops.yaml`
(no path segments), so no escaping subtlety to worry about beyond what's
shown.

## 4. Controller changes — `internal/controller/forgejorepository_controller.go`

Follow `reconcileDeployKeyState`/`reconcileDeployKey`/`deployKeySecretName`
as the template. Add:

```go
const (
	sopsSecretDataField       = "age.agekey"       // matches the old Job's secret key name
	sopsPublicKeyConfigMapKey = "age.pub"
	sopsFilePath              = ".sops.yaml"
)
```

- `sopsSecretName(repo) string` / `sopsConfigMapName(repo) string` — same
  shape as `deployKeySecretName`, defaulting to `"sops-age"` /
  `"sops-age-public-key"` respectively when `spec.sops.secretName` /
  `spec.sops.publicKeyConfigMapName` are unset. **Do not** default
  `SecretName` to something derived from `repo.Name` the way
  `deployKeySecretName` does — the fixed `"sops-age"` default is load-bearing
  (cluster-projects' Flux `Kustomization.spec.decryption.secretRef.name` is
  a hardcoded literal, not templated per-project).

- `reconcileSopsState(ctx, apiClient, owner, repo) error`, called from
  `Reconcile` right after `reconcileDeployKeyState`:
  - If `repo.Spec.Sops == nil || !repo.Spec.Sops.Enabled`: if
    `repo.Status.SopsSecretName == ""`, no-op and return. Otherwise this is
    a disable-after-enable transition — delete the Secret and ConfigMap
    named in `repo.Status.SopsSecretName`/`SopsPublicKeyConfigMapName`
    (tolerate `apierrors.IsNotFound`, same as
    `reconcileDeployKeyState`'s teardown branch), clear both status fields,
    and return. **Do not** attempt to remove or revert the committed
    `.sops.yaml` file — unlike a deploy key (a revocable credential on
    Forgejo), it's just a text file in git history; force-pushing a commit
    to delete it on disable would be a surprising, unrequested repository
    mutation and isn't worth the complexity. Leaving a stale `.sops.yaml`
    behind after disabling `spec.sops` is an acceptable, documented
    trade-off.
  - If enabled: ensure the Secret exists (`r.Get`; on `IsNotFound`,
    generate via `agekey.Generate()` and `r.Create` with
    `controllerutil.SetControllerReference`, `StringData` key
    `sopsSecretDataField`, exactly mirroring
    `createDeployKeySecret`/`reconcileDeployKey`'s existing-vs-create
    branch). Read back the public key: if the Secret already existed, it
    was NOT just generated, so the public key must be re-derived — age
    private key text doesn't trivially round-trip to its public key
    without reparsing, so either (a) re-parse the stored private key text
    with `age.ParseIdentities` to recover the public key, or (b) **store
    the public key in the ConfigMap as the source of truth** and read it
    back from there on subsequent reconciles instead of re-deriving from
    the Secret. Prefer (b) — it avoids a second age dependency surface and
    mirrors how `reconcileDeployKey` already reads `identity.pub` back
    from a Secret field rather than re-deriving it; the difference here is
    only that the public half lives in the ConfigMap, not a second Secret
    field, since this feature needs it as a release artifact anyway (ensure
    the ConfigMap exists alongside/right after the Secret, same
    create-if-missing logic, and read its `sopsPublicKeyConfigMapKey` value
    back on every reconcile as the public key for step 3).
  - Once the public key is known: call a new `reconcileSopsFile` to ensure
    `.sops.yaml` is committed (see below). Update
    `repo.Status.SopsSecretName`/`SopsPublicKeyConfigMapName` to the
    resolved names.

- `reconcileSopsFile(ctx, apiClient, owner, repo, publicKey string) error`:
  - Desired content:
    ```yaml
    creation_rules:
      - age: <publicKey>
    ```
    (no `path_regex` — keep it unrestricted, matching this operator's
    general stance of not imposing cluster-projects-specific conventions
    that live one repo over.)
  - Branch to commit to: `repo.Spec.DefaultBranch`, defaulting to `"main"`
    when empty (same default `cluster-projects/templates/repository.yaml`
    already applies before setting `spec.defaultBranch`).
  - `apiClient.GetContents(ctx, owner, repo.Name, sopsFilePath, branch)`:
    - `ErrNotFound` → `CreateFile` with a commit message like
      `"chore: add .sops.yaml"`.
    - success → base64-decode `.Content`, compare against desired content;
      if different, `UpdateFile` with the returned `.SHA` (message e.g.
      `"chore: update .sops.yaml"`); if identical, no-op. (This keeps
      reconciles idempotent and also self-heals if someone hand-edits the
      committed public key out from under the Secret — unlikely, but
      cheap to handle given the check is already needed for idempotency.)
    - any other error → return it (ordinary reconcile-error path, will
      retry).
  - Note the documented limitation: this requires the repository to
    already have at least one commit on `branch` (Contents API needs an
    existing branch to commit onto). Every repository cluster-projects
    creates already sets `autoInit: true` unconditionally, so this holds
    in practice; a repository created with `autoInit: false` and nothing
    pushed yet will error here and retry on the next reconcile once it has
    content — don't add special-case handling for that, it's the same
    "referenced thing doesn't exist yet, retry" shape every other
    reconciler here already tolerates.

- RBAC markers: add a `+kubebuilder:rbac:groups="",resources=configmaps,...`
  marker (the existing secrets marker on this controller already has
  `create`; configmaps needs the same `get;list;watch;create;update;delete`
  set). Re-run `make manifests` after.

- `SetupWithManager`: add `.Owns(&corev1.ConfigMap{})` alongside the
  existing `.Owns(&corev1.Secret{})`.

- `reconcileDelete`: no change needed — the Secret/ConfigMap are
  owner-referenced to the `ForgejoRepository` (`SetControllerReference`),
  so ordinary Kubernetes garbage collection removes them when the CR is
  deleted, same as the deploy-key Secret today. The committed
  `.sops.yaml` is likewise left in place on delete, consistent with the
  disable-path reasoning above.

## 5. Tests

Add specs to `internal/controller/forgejorepository_controller_test.go`
(same file the deploy-key specs already live in), following their shape
exactly — they assert against the real `envtest` + real Forgejo container
from `suite_test.go`, not mocks:

- Enabling `spec.sops` creates a Secret (`sopsSecretDataField` present,
  parses as a valid age identity) and ConfigMap
  (`sopsPublicKeyConfigMapKey` present, matches the identity's recipient)
  in the `ForgejoRepository`'s namespace.
- `.sops.yaml` actually exists on the real Forgejo repo's default branch
  (fetch via a second `forgejoapi.Client`, same pattern the existing tests
  use to assert against the real server) and its content matches the
  ConfigMap's public key.
- Reconciling twice does not change the Secret's content, the ConfigMap's
  content, or create a second commit (assert the file's `sha` is stable
  across both reconciles).
- Disabling `spec.sops` after it was enabled removes the Secret and
  ConfigMap but leaves `.sops.yaml` in the repo untouched.
- Deleting the `ForgejoRepository` while `spec.sops.enabled: true` removes
  the Secret/ConfigMap (owner-reference GC — envtest does run the GC
  controller, unlike the namespace controller already noted as a gap in
  `CLAUDE.md`).

## Follow-up in `cluster-projects` (not part of this spec, for awareness only)

Once this ships and a release is cut, `cluster-projects` would:
- delete `templates/sops-age-key.yaml` entirely (both the original keygen
  Job chain and the namespace-bootstrap workaround Job/RBAC added as a
  stopgap),
- set `spec.sops.enabled: true` on the `ForgejoRepository` block in
  `templates/repository.yaml` whenever `project.kustomize.sops` is true,
- leave `templates/kustomize.yaml`'s
  `decryption.secretRef.name: sops-age` as-is, since it already matches
  this spec's default Secret name.

That work is out of scope here and should happen as a separate change in
that repo once this lands.
