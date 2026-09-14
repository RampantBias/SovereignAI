package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/SovereignAI/cmd/cli/cli"
	"github.com/SovereignAI/internal/api/v1/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type bearerCredentials struct{ token string }

func (c bearerCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (bearerCredentials) RequireTransportSecurity() bool { return true }

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	flags := flag.NewFlagSet("sovctl", flag.ExitOnError)
	server := flags.String("server", envOr("SOVEREIGN_API_ADDRESS", "127.0.0.1:8080"), "gRPC API address")
	caFile := flags.String("ca-file", os.Getenv("SOVEREIGN_API_CA_FILE"), "PEM CA certificate used by the API")
	serverName := flags.String("server-name", envOr("SOVEREIGN_API_SERVER_NAME", "sovereign-api.sovereign-orchestrator-system.svc"), "TLS server name")
	tokenFile := flags.String("token-file", os.Getenv("SOVEREIGN_API_TOKEN_FILE"), "file containing a short-lived bearer token")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: sovctl [connection flags] <command> [arguments]\n")
		flags.PrintDefaults()
	}
	_ = flags.Parse(os.Args[1:])

	if strings.TrimSpace(*caFile) == "" || strings.TrimSpace(*tokenFile) == "" {
		log.Fatal("--ca-file and --token-file are required")
	}
	tokenBytes, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatalf("unable to read bearer token file: %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		log.Fatal("bearer token file must contain exactly one non-empty token")
	}
	tlsCredentials, err := credentials.NewClientTLSFromFile(*caFile, *serverName)
	if err != nil {
		log.Fatalf("unable to load API CA certificate: %v", err)
	}
	conn, err := grpc.NewClient(*server, grpc.WithTransportCredentials(tlsCredentials), grpc.WithPerRPCCredentials(bearerCredentials{token: token}))
	if err != nil {
		log.Fatalf("unable to configure gRPC client for %s: %v", *server, err)
	}
	defer conn.Close()

	client := pb.NewOrchestratorServiceClient(conn)
	if args := flags.Args(); len(args) > 0 {
		root := cli.NewRootCmd(client)
		root.SetArgs(args)
		if err := root.ExecuteContext(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
		return
	}
	cli.RunShell(ctx, cli.ShellDependencies{Client: client, Cancel: cancel})
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
