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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	forgejoschrankedev1alpha1 "github.com/theodor-franke/forgejo-operator/api/v1alpha1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.
//
// Controller tests run against a real Forgejo server started with
// testcontainers-go (see startForgejoContainer below) rather than a mock, so
// they exercise the actual Forgejo REST API.

const (
	forgejoImage         = "codeberg.org/forgejo/forgejo:15"
	forgejoAdminUsername = "operator-admin"
	forgejoAdminPassword = "OperatorTest123!"
	forgejoAdminEmail    = "operator-admin@example.com"

	// forgejoOAuth2SourceID is the ID of the single OAuth2 authentication
	// source provisioned in BeforeSuite (below). It is the only auth source
	// created for the whole suite, so it is always assigned ID 1 and is
	// always "the first" one the operator will find.
	forgejoOAuth2SourceID = 1
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client

	forgejoContainer testcontainers.Container
	forgejoBaseURL   string
	forgejoSSHHost   string
	forgejoSSHPort   int32
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = forgejoschrankedev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata"),
		},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("starting a real Forgejo instance via testcontainers")
	forgejoBaseURL = startForgejoContainer(ctx)
})

var _ = AfterSuite(func() {
	By("tearing down the Forgejo test instance")
	if forgejoContainer != nil {
		Expect(forgejoContainer.Terminate(context.Background())).To(Succeed())
	}

	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// startForgejoContainer starts a real, throwaway Forgejo server (sqlite
// backend, install lock pre-set) and provisions an admin user, returning its
// externally reachable base URL. Tests authenticate against it with
// forgejoAdminUsername/forgejoAdminPassword instead of mocking the Forgejo API.
func startForgejoContainer(ctx context.Context) string {
	req := testcontainers.ContainerRequest{
		Image:        forgejoImage,
		ExposedPorts: []string{"3000/tcp", "22/tcp"},
		Env: map[string]string{
			"USER_UID":                             "1000",
			"USER_GID":                             "1000",
			"GITEA__database__DB_TYPE":             "sqlite3",
			"GITEA__security__INSTALL_LOCK":        "true",
			"GITEA__log__LEVEL":                    "warn",
			"GITEA__server__DISABLE_SSH":           "false",
			"GITEA__service__DISABLE_REGISTRATION": "true",
		},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/api/v1/version").
				WithPort("3000/tcp").
				WithStartupTimeout(2*time.Minute),
			wait.ForListeningPort("22/tcp").
				WithStartupTimeout(2*time.Minute),
		),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	Expect(err).NotTo(HaveOccurred())
	forgejoContainer = container

	execAsGit(ctx, container, []string{
		"forgejo", "admin", "user", "create",
		"--username", forgejoAdminUsername,
		"--password", forgejoAdminPassword,
		"--email", forgejoAdminEmail,
		"--admin",
		"--must-change-password=false",
	}, "creating Forgejo admin user")

	// The group-team-map feature only has a web UI / CLI, no REST API (see
	// internal/forgejoweb), so tests need a real OAuth2 source to exercise
	// it against. "github" needs no reachable discovery URL, unlike
	// openidConnect, so it starts up without any network dependency.
	execAsGit(ctx, container, []string{
		"forgejo", "admin", "auth", "add-oauth",
		"--name", "operator-test-oidc",
		"--provider", "github",
		"--key", "test-client-id",
		"--secret", "test-secret",
	}, "creating Forgejo OAuth2 authentication source")

	host, err := container.Host(ctx)
	Expect(err).NotTo(HaveOccurred())
	mappedPort, err := container.MappedPort(ctx, "3000")
	Expect(err).NotTo(HaveOccurred())

	sshMappedPort, err := container.MappedPort(ctx, "22")
	Expect(err).NotTo(HaveOccurred())
	forgejoSSHHost = host
	forgejoSSHPort = int32(sshMappedPort.Int())

	return fmt.Sprintf("http://%s:%s", host, mappedPort.Port())
}

// execAsGit runs cmd inside the Forgejo container as the "git" user (the
// image refuses to run its own CLI as root) and fails the suite with ctx if
// it exits non-zero.
func execAsGit(ctx context.Context, container testcontainers.Container, cmd []string, what string) {
	exitCode, reader, err := container.Exec(ctx, cmd, tcexec.WithUser("git"))
	Expect(err).NotTo(HaveOccurred())
	if exitCode != 0 {
		output, _ := io.ReadAll(reader)
		Fail(fmt.Sprintf("%s failed (exit %d): %s", what, exitCode, output))
	}
}

// loginSourceConfig mirrors the subset of Forgejo's internal OAuth2 source
// config (source.OAuth2Config) needed to verify the group-team map that
// internal/forgejoweb writes through the web admin UI. There is no REST API
// to read it back, so tests read it directly out of the sqlite database
// inside the container instead.
type loginSourceConfig struct {
	GroupTeamMap string `json:"GroupTeamMap"`
}

// readStoredGroupTeamMapRaw reads the raw (still JSON-string-encoded)
// GroupTeamMap field stored for the test OAuth2 authentication source,
// without assuming it decodes to valid JSON - useful for asserting on
// corrupted/non-JSON content that readStoredGroupTeamMap can't represent.
func readStoredGroupTeamMapRaw(ctx context.Context) string {
	query := fmt.Sprintf("select cfg from login_source where id=%d", forgejoOAuth2SourceID)
	exitCode, reader, err := forgejoContainer.Exec(ctx,
		[]string{"sqlite3", "/data/gitea/gitea.db", query},
		tcexec.Multiplexed(),
	)
	Expect(err).NotTo(HaveOccurred())
	output, err := io.ReadAll(reader)
	Expect(err).NotTo(HaveOccurred())
	if exitCode != 0 {
		Fail(fmt.Sprintf("reading login_source config failed (exit %d): %s", exitCode, output))
	}

	var cfg loginSourceConfig
	Expect(json.Unmarshal(bytes.TrimSpace(output), &cfg)).To(Succeed())
	return cfg.GroupTeamMap
}

// readStoredGroupTeamMap reads and double-decodes the GroupTeamMap stored
// for the test OAuth2 authentication source, returning it as
// group -> organization -> []team, the same shape computeGroupTeamMap builds.
func readStoredGroupTeamMap(ctx context.Context) map[string]map[string][]string {
	raw := readStoredGroupTeamMapRaw(ctx)

	result := map[string]map[string][]string{}
	if raw == "" {
		return result
	}
	Expect(json.Unmarshal([]byte(raw), &result)).To(Succeed())
	return result
}

// corruptStoredGroupTeamMap overwrites the GroupTeamMap field stored for the
// test OAuth2 authentication source with rawValue, bypassing this operator
// entirely (straight sqlite3 UPDATE inside the container). This simulates
// the field being changed out from under the operator by something other
// than a ForgejoOrganization reconcile - e.g. a human editing the same auth
// source in the admin UI, or the literal string "map[]" (Go's fmt
// representation of an empty map) showing up in the field, as observed
// against a real instance; this operator's own group-team map writes always
// go through encoding/json and can never produce that string. Used to prove
// the periodic resync (organizationReadyRequeueInterval) self-heals drift
// that a spec-triggered reconcile alone would never notice.
func corruptStoredGroupTeamMap(ctx context.Context, rawValue string) {
	selectQuery := fmt.Sprintf("select cfg from login_source where id=%d", forgejoOAuth2SourceID)
	exitCode, reader, err := forgejoContainer.Exec(ctx,
		[]string{"sqlite3", "/data/gitea/gitea.db", selectQuery},
		tcexec.Multiplexed(),
	)
	Expect(err).NotTo(HaveOccurred())
	output, err := io.ReadAll(reader)
	Expect(err).NotTo(HaveOccurred())
	if exitCode != 0 {
		Fail(fmt.Sprintf("reading login_source config failed (exit %d): %s", exitCode, output))
	}

	var cfg map[string]any
	Expect(json.Unmarshal(bytes.TrimSpace(output), &cfg)).To(Succeed())
	cfg["GroupTeamMap"] = rawValue
	newCfg, err := json.Marshal(cfg)
	Expect(err).NotTo(HaveOccurred())

	escaped := strings.ReplaceAll(string(newCfg), "'", "''")
	updateQuery := fmt.Sprintf("update login_source set cfg='%s' where id=%d", escaped, forgejoOAuth2SourceID)
	exitCode, reader, err = forgejoContainer.Exec(ctx,
		[]string{"sqlite3", "/data/gitea/gitea.db", updateQuery},
		tcexec.Multiplexed(),
	)
	Expect(err).NotTo(HaveOccurred())
	output, err = io.ReadAll(reader)
	Expect(err).NotTo(HaveOccurred())
	if exitCode != 0 {
		Fail(fmt.Sprintf("corrupting login_source config failed (exit %d): %s", exitCode, output))
	}
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
