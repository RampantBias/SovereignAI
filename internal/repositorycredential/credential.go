package repositorycredential

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/SovereignAI/internal/controllermeta"
	corev1 "k8s.io/api/core/v1"
)

// HTTPS is the parsed, in-memory form of the repository credential-store entry.
// Callers must not include this value in logs or error messages.
type HTTPS struct {
	ScopeURL string
	Username string
	Password string
}

// Parse validates the repository credential Secret contract shared by utility
// operations and Argo repository provisioning.
func Parse(source *corev1.Secret) (HTTPS, error) {
	if source.Type != corev1.SecretTypeOpaque {
		return HTTPS{}, fmt.Errorf("repository credential Secret must have type %q", corev1.SecretTypeOpaque)
	}
	if len(source.Data) != 1 {
		return HTTPS{}, fmt.Errorf("repository credential Secret must contain exactly the %q key", controllermeta.RepositoryCredentialKey)
	}

	contents, ok := source.Data[controllermeta.RepositoryCredentialKey]
	if !ok || len(contents) == 0 {
		return HTTPS{}, fmt.Errorf("repository credential Secret must contain a non-empty %q key", controllermeta.RepositoryCredentialKey)
	}
	if !utf8.Valid(contents) {
		return HTTPS{}, fmt.Errorf("repository credential entry must be valid UTF-8")
	}

	entry := strings.ReplaceAll(string(contents), "\r\n", "\n")
	entry = strings.TrimSuffix(entry, "\n")
	if entry == "" || strings.ContainsAny(entry, "\r\n") || strings.TrimSpace(entry) != entry {
		return HTTPS{}, fmt.Errorf("repository credential Secret must contain exactly one credential-store entry")
	}

	credentialURL, err := url.Parse(entry)
	if err != nil {
		return HTTPS{}, fmt.Errorf("repository credential entry must be a valid HTTPS credential-store URL")
	}
	if credentialURL.User == nil {
		return HTTPS{}, fmt.Errorf("repository credential entry must be one HTTPS URL with an encoded username and secret")
	}
	password, hasPassword := credentialURL.User.Password()
	if !strings.EqualFold(credentialURL.Scheme, "https") ||
		credentialURL.Host == "" ||
		credentialURL.User.Username() == "" ||
		!hasPassword ||
		password == "" ||
		credentialURL.RawQuery != "" ||
		credentialURL.Fragment != "" {
		return HTTPS{}, fmt.Errorf("repository credential entry must be one HTTPS URL with an encoded username and secret")
	}

	username := credentialURL.User.Username()
	credentialURL.User = nil
	return HTTPS{ScopeURL: credentialURL.String(), Username: username, Password: password}, nil
}
