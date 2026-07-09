package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
)

func main() {
	inputPath := flag.String("input", "/workspace/control/input.json", "agent input contract")
	resultPath := flag.String("result", "/workspace/control/result.json", "agent result contract")
	timeout := flag.Duration("timeout", 30*time.Minute, "maximum agent execution time")
	grace := flag.Duration("termination-grace", 10*time.Second, "grace before killing an interrupted agent")
	flag.Parse()

	input, err := agentcontract.ReadInput(*inputPath)
	if err != nil {
		fail(*resultPath, fmt.Errorf("invalid input: %w", err))
	}
	command, err := configuredCommand()
	if err != nil {
		fail(*resultPath, err)
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	if err := run(ctx, command, *inputPath, *resultPath, *grace); err != nil {
		fail(*resultPath, err)
	}
	result, err := agentcontract.ReadResultForInput(*resultPath, input)
	if err != nil {
		fail(*resultPath, fmt.Errorf("agent returned an invalid result: %w", err))
	}
	if result.Outcome != "Succeeded" {
		log.Printf("agent completed with outcome %s: %s", result.Outcome, result.Message)
		os.Exit(1)
	}
}

func configuredCommand() ([]string, error) {
	value := os.Getenv("SOVEREIGN_AGENT_EXECUTABLE")
	if value == "" {
		return nil, fmt.Errorf("SOVEREIGN_AGENT_EXECUTABLE must contain a JSON command array")
	}
	var command []string
	if err := json.Unmarshal([]byte(value), &command); err != nil || len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("SOVEREIGN_AGENT_EXECUTABLE must be a non-empty JSON command array")
	}
	return command, nil
}

func run(ctx context.Context, command []string, inputPath, resultPath string, grace time.Duration) error {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = append(os.Environ(), "SOVEREIGN_INPUT_PATH="+inputPath, "SOVEREIGN_RESULT_PATH="+resultPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("agent exited: %w", err)
		}
		return nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-time.After(grace):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		case <-done:
		}
		return fmt.Errorf("agent interrupted: %w", ctx.Err())
	}
}

func fail(resultPath string, err error) {
	log.Print(err)
	result := agentcontract.Result{SchemaVersion: agentcontract.Version, Outcome: "Failed", Message: err.Error()}
	if writeErr := agentcontract.WriteResult(resultPath, result); writeErr != nil && !errors.Is(writeErr, os.ErrPermission) {
		log.Printf("write failure result: %v", writeErr)
	}
	os.Exit(1)
}
