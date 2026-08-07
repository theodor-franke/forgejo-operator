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

// Package sshkey generates SSH deploy-key pairs and discovers a Forgejo
// instance's SSH host key for known_hosts, using only the standard library
// plus golang.org/x/crypto/ssh (no shelling out to ssh-keygen/ssh-keyscan).
package sshkey

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KeyPair is an SSH keypair in the text formats Forgejo and OpenSSH expect.
type KeyPair struct {
	// PublicKey is in OpenSSH authorized_keys format, e.g.
	// "ssh-ed25519 AAAA...  <comment>\n".
	PublicKey string
	// PrivateKey is a PEM-encoded OpenSSH private key.
	PrivateKey string
}

// Generate creates a new ed25519 SSH keypair. comment is embedded in the
// public key line, the same way ssh-keygen embeds one.
func Generate(comment string) (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshkey: generating ed25519 keypair: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("sshkey: converting public key: %w", err)
	}
	publicKeyLine := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshPub)), "\n")
	if comment != "" {
		publicKeyLine += " " + comment
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, fmt.Errorf("sshkey: marshaling private key: %w", err)
	}

	return &KeyPair{
		PublicKey:  publicKeyLine + "\n",
		PrivateKey: string(pem.EncodeToMemory(block)),
	}, nil
}

// errHostKeyCaptured aborts the SSH handshake the instant a host key is
// offered: SSH servers send their host key before any authentication step,
// so no credentials are needed to discover it, and there's no reason to let
// the (doomed-to-fail) handshake continue past that point.
var errHostKeyCaptured = errors.New("sshkey: host key captured")

// ScanHostKey connects to host:port and returns a known_hosts line for the
// SSH host key it offers.
func ScanHostKey(ctx context.Context, host string, port int32) (string, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("sshkey: connecting to %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var hostKey ssh.PublicKey
	_, _, _, err = ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User: "git",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = key
			return errHostKeyCaptured
		},
		Timeout: 10 * time.Second,
	})
	if hostKey == nil {
		return "", fmt.Errorf("sshkey: SSH handshake with %s did not offer a host key: %w", addr, err)
	}

	return knownhosts.Line([]string{knownhosts.Normalize(addr)}, hostKey) + "\n", nil
}
