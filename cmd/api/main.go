package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SovereignAI/internal/api/requestidentity"
	v1 "github.com/SovereignAI/internal/api/v1"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var scheme = runtime.NewScheme()

func init() {
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Println("Initializing SovereignAI API Server...")
	restConfig := ctrl.GetConfigOrDie()
	k8sClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("unable to initialize direct kubernetes client: %v", err)
	}

	recorder, closeAudit, auditMode, err := audit.OpenConfigured(ctx, audit.ConfigFromEnv())
	if err != nil {
		log.Fatalf("unable to initialize audit recorder: %v", err)
	}
	defer closeAudit()
	if auditMode == "memory" {
		log.Print("SOVEREIGN_AUDIT_DSN is unset; using non-durable in-memory audit recorder")
	} else {
		log.Printf("Audit recorder initialized with %s backend", auditMode)
	}

	publicKeyFile := requiredEnv("SOVEREIGN_API_AUTH_PUBLIC_KEY_FILE")
	publicKey, err := requestidentity.LoadEd25519PublicKey(publicKeyFile)
	if err != nil {
		log.Fatalf("unable to load human authentication public key: %v", err)
	}
	maxLifetime, err := time.ParseDuration(envOr("SOVEREIGN_API_AUTH_MAX_TOKEN_LIFETIME", "24h"))
	if err != nil {
		log.Fatalf("invalid SOVEREIGN_API_AUTH_MAX_TOKEN_LIFETIME: %v", err)
	}
	authInterceptor, err := requestidentity.NewJWTUnaryServerInterceptor(requestidentity.JWTConfig{
		PublicKey: publicKey, Issuer: requiredEnv("SOVEREIGN_API_AUTH_ISSUER"),
		Audience: requiredEnv("SOVEREIGN_API_AUTH_AUDIENCE"), MaxLifetime: maxLifetime,
	})
	if err != nil {
		log.Fatalf("unable to initialize API authentication: %v", err)
	}
	transportCredentials, err := credentials.NewServerTLSFromFile(
		requiredEnv("SOVEREIGN_API_TLS_CERT_FILE"), requiredEnv("SOVEREIGN_API_TLS_KEY_FILE"))
	if err != nil {
		log.Fatalf("unable to initialize API TLS: %v", err)
	}

	listenAddr := ":8080"
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("failed to bind to TCP port %s: %v", listenAddr, err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(transportCredentials), grpc.UnaryInterceptor(v1.ContextUploadInterceptor(authInterceptor)))
	apiServer := v1.NewServer(k8sClient, recorder)
	pb.RegisterOrchestratorServiceServer(grpcServer, apiServer)
	reflection.Register(grpcServer)

	errChan := make(chan error, 1)
	go func() {
		log.Printf("API server control plane is actively listening with TLS on %s\n", listenAddr)
		if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("Received termination interrupt. Cleaning network sockets...")
		grpcServer.GracefulStop()
		log.Println("API Server clean shutdown finalized.")
	case err := <-errChan:
		log.Fatalf("gRPC core network error: %v", err)
	}
}

func requiredEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
