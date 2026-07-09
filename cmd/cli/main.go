package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/SovereignAI/cmd/cli/cli"
	"github.com/SovereignAI/internal/api/v1/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	serverAddress := "127.0.0.1:8080"
	conn, err := grpc.NewClient(serverAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to open gRPC pipeline to %s: %v", serverAddress, err)
	}
	defer conn.Close()
	// TODO: Perform Authentication to cluster Orchestrator service

	client := pb.NewOrchestratorServiceClient(conn)

	// Spin up shell
	shellDependencies := cli.ShellDependencies{
		Client: client,
		Cancel: cancel,
	}
	cli.RunShell(ctx, shellDependencies)
}
