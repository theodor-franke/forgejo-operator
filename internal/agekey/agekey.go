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

// Package agekey generates SOPS-compatible age keypairs, using
// filippo.io/age (no shelling out to age-keygen).
package agekey

import (
	"fmt"
	"strings"
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

// PublicKey recovers the age1... recipient string from privateKey, which may
// be either a bare AGE-SECRET-KEY-1... identity or the full age-keygen file
// text Generate produces (comment lines are skipped).
func PublicKey(privateKey string) (string, error) {
	identities, err := age.ParseIdentities(strings.NewReader(privateKey))
	if err != nil {
		return "", fmt.Errorf("agekey: parsing identity: %w", err)
	}
	for _, identity := range identities {
		if x25519, ok := identity.(*age.X25519Identity); ok {
			return x25519.Recipient().String(), nil
		}
	}
	return "", fmt.Errorf("agekey: no X25519 identity found")
}
