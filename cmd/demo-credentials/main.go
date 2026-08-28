package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const namespace = "sovereign-orchestrator-system"

func main() {
	outputDir := flag.String("output-dir", "", "secure directory outside the repository for the client CA and token")
	subject := flag.String("subject", "demo-developer", "authenticated demo subject")
	validFor := flag.Duration("valid-for", 15*time.Minute, "short-lived token validity")
	flag.Parse()
	if *outputDir == "" {
		log.Fatal("--output-dir is required")
	}
	if *validFor <= 0 || *validFor > 15*time.Minute {
		log.Fatal("--valid-for must be greater than zero and no longer than 15m")
	}
	if err := os.MkdirAll(*outputDir, 0o700); err != nil {
		log.Fatal(err)
	}

	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "SovereignAI Demo API CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		log.Fatal(err)
	}
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "sovereign-api.sovereign-orchestrator-system.svc"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		DNSNames:    []string{"sovereign-api", "sovereign-api.sovereign-orchestrator-system", "sovereign-api.sovereign-orchestrator-system.svc", "sovereign-api.sovereign-orchestrator-system.svc.cluster.local", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, serverPublic, caPrivate)
	if err != nil {
		log.Fatal(err)
	}
	serverPrivateDER, err := x509.MarshalPKCS8PrivateKey(serverPrivate)
	if err != nil {
		log.Fatal(err)
	}

	authPublic, authPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	authPublicDER, err := x509.MarshalPKIXPublicKey(authPublic)
	if err != nil {
		log.Fatal(err)
	}
	authPrivateDER, err := x509.MarshalPKCS8PrivateKey(authPrivate)
	if err != nil {
		log.Fatal(err)
	}
	claims := struct {
		Groups []string `json:"groups"`
		jwt.RegisteredClaims
	}{
		Groups: []string{"contract-maintainers"},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "sovereign-demo", Subject: *subject, Audience: jwt.ClaimStrings{"sovereign-api"},
			ExpiresAt: jwt.NewNumericDate(now.Add(*validFor)), IssuedAt: jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)), ID: randomID(),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(authPrivate)
	if err != nil {
		log.Fatal(err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverPrivateDER})
	authPublicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: authPublicDER})
	authPrivatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: authPrivateDER})

	writeFile(filepath.Join(*outputDir, "sovereign-api-ca.crt"), caPEM, 0o644)
	writeFile(filepath.Join(*outputDir, "sovereign-human-signing-key.pem"), authPrivatePEM, 0o600)
	writeFile(filepath.Join(*outputDir, "sovereign-human.jwt"), []byte(token+"\n"), 0o600)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		log.Fatal(err)
	}
	kubeClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	upsertSecret(ctx, kubeClient, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sovereign-api-tls", Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: serverCertPEM, corev1.TLSPrivateKeyKey: serverKeyPEM, "ca.crt": caPEM},
	})
	upsertSecret(ctx, kubeClient, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sovereign-human-auth", Namespace: namespace},
		Type:       corev1.SecretTypeOpaque, Data: map[string][]byte{"public.pem": authPublicPEM},
	})
	fmt.Printf("Updated API TLS/auth Secrets in namespace %s.\n", namespace)
	fmt.Printf("Client CA: %s\n", filepath.Join(*outputDir, "sovereign-api-ca.crt"))
	fmt.Printf("Client token: %s (expires %s)\n", filepath.Join(*outputDir, "sovereign-human.jwt"), now.Add(*validFor).Format(time.RFC3339))
}

func upsertSecret(ctx context.Context, kubeClient client.Client, desired *corev1.Secret) {
	var current corev1.Secret
	err := kubeClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &current)
	switch {
	case apierrors.IsNotFound(err):
		if err := kubeClient.Create(ctx, desired); err != nil {
			log.Fatal(err)
		}
	case err != nil:
		log.Fatal(err)
	default:
		current.Type = desired.Type
		current.Data = desired.Data
		if err := kubeClient.Update(ctx, &current); err != nil {
			log.Fatal(err)
		}
	}
}

func writeFile(path string, content []byte, mode os.FileMode) {
	if err := os.WriteFile(path, content, mode); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		log.Fatal(err)
	}
}

func serial() *big.Int {
	value, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatal(err)
	}
	return value
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		log.Fatal(err)
	}
	return fmt.Sprintf("%x", value)
}
