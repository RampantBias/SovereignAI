package repositorycredential

import (
	"strings"
	"testing"

	"github.com/SovereignAI/internal/controllermeta"
	corev1 "k8s.io/api/core/v1"
)

func TestParseRepositoryCredential(t *testing.T) {
	secret := &corev1.Secret{
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			controllermeta.RepositoryCredentialKey: []byte("https://demo-user:token%3Awith%40symbols@example.test\n"),
		},
	}
	credential, err := Parse(secret)
	if err != nil {
		t.Fatal(err)
	}
	if credential.ScopeURL != "https://example.test" || credential.Username != "demo-user" || credential.Password != "token:with@symbols" {
		t.Fatalf("unexpected parsed credential: %#v", credential)
	}
}

func TestParseRepositoryCredentialDoesNotExposeSecret(t *testing.T) {
	secret := &corev1.Secret{
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			controllermeta.RepositoryCredentialKey: []byte("https://demo-user:do-not-expose@example.test\nhttps://other:secret@example.test\n"),
		},
	}
	_, err := Parse(secret)
	if err == nil {
		t.Fatal("expected multiple credential entries to be rejected")
	}
	if strings.Contains(err.Error(), "do-not-expose") || strings.Contains(err.Error(), "other:secret") {
		t.Fatalf("credential validation exposed secret material: %v", err)
	}
}
