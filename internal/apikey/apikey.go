// Package apikey defines the API key format: "sb_<orgid>_<secret>".
// The org prefix is a routing hint only; the child process verifies the
// secret (as a SHA-256 hash) against its own org database.
package apikey

import (
	"crypto/rand"
	"encoding/base64"
	"regexp"
	"strings"
)

const prefix = "sb_"

var orgIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidOrgID(id string) bool {
	return orgIDRe.MatchString(id)
}

// Parse splits a key into its org prefix and secret. It does not verify
// the secret; that happens in the org's child process.
func Parse(key string) (orgID, secret string, ok bool) {
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return "", "", false
	}
	orgID, secret, ok = strings.Cut(rest, "_")
	if !ok || !ValidOrgID(orgID) || secret == "" {
		return "", "", false
	}
	return orgID, secret, true
}

// New generates a fresh random secret for the given org.
func New(orgID string) (string, error) {
	if !ValidOrgID(orgID) {
		return "", &InvalidOrgIDError{OrgID: orgID}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + orgID + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

type InvalidOrgIDError struct {
	OrgID string
}

func (e *InvalidOrgIDError) Error() string {
	return "invalid org id: " + e.OrgID
}
