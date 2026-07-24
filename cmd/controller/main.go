package main

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/policy"
	"github.com/SovereignAI/internal/validation"
	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	_ = v1alpha1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
}

func main() {
	// Controller-runtime signal observer context
	ctx := ctrl.SetupSignalHandler()
	ctrl.SetLogger(newLogger())

	log.Println("Initializing Sovereign Workflow Controller Substrate...")

	systemNamespace := env("SOVEREIGN_SYSTEM_NAMESPACE", "sovereign-orchestrator-system")
	storageClass := env("SOVEREIGN_STORAGE_CLASS", "local-path")

	// Initialize the blocking Kubernetes Manager engine
	restConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                  scheme,
		LeaderElection:          envBool("SOVEREIGN_LEADER_ELECTION", true), // HA
		LeaderElectionID:        "controller.aim.sovereign.io",
		LeaderElectionNamespace: systemNamespace,
		Metrics:                 metricsserver.Options{BindAddress: env("SOVEREIGN_METRICS_ADDRESS", ":8081")},
		HealthProbeBindAddress:  env("SOVEREIGN_HEALTH_ADDRESS", ":8082"),
		GracefulShutdownTimeout: durationPtr(30 * time.Second),
	})
	if err != nil {
		log.Fatalf("unable to start manager platform: %v", err)
	}

	// Open Auditor, DSN must be set if we aren't testing
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

	// Initialize in-memory OPA
	policyEvaluator, err := policy.NewMVP(ctx)
	if err != nil {
		log.Fatalf("unable to prepare MVP policy: %v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("unable to create validation client: %v", err)
	}

	// Inject Argo/Kustomize deployment into ephemeral workspace
	validationProvider := validation.NewArgoKustomize(dynamicClient, env("SOVEREIGN_ARGO_NAMESPACE", "argocd"))

	// Controller initialization
	reconcilers := []interface{ SetupWithManager(ctrl.Manager) error }{
		&controllers.SovereignProjectReconciler{
			Client: mgr.GetClient(),
			Audit:  recorder},
		&controllers.WorkflowReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			Audit:  recorder, StorageClass: storageClass},
		&controllers.StepAttemptReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			Audit:  recorder},
		&controllers.AgentRunReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			Audit:          recorder,
			CollectorImage: env("SOVEREIGN_COLLECTOR_IMAGE", "sovereign-artifact-collector:dev")},
		&controllers.UtilityOperationReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			Audit:          recorder,
			Policy:         policyEvaluator,
			CollectorImage: env("SOVEREIGN_COLLECTOR_IMAGE", "sovereign-artifact-collector:dev"),
			UtilityImage:   env("SOVEREIGN_UTILITY_IMAGE", "sovereign-utility-runner:dev")},
		&controllers.ApprovalRequestReconciler{
			Client: mgr.GetClient(),
			Audit:  recorder},
		&controllers.ArtifactReconciler{
			Client: mgr.GetClient(),
			Audit:  recorder},
		&controllers.HumanSessionReconciler{
			Client:          mgr.GetClient(),
			Scheme:          mgr.GetScheme(),
			Audit:           recorder,
			CodeServerImage: env("SOVEREIGN_CODE_SERVER_IMAGE", "ghcr.io/coder/code-server:4.99.4")},
		&controllers.ValidationRunReconciler{
			Client:   mgr.GetClient(),
			Provider: validationProvider,
			Audit:    recorder},
		&controllers.InferenceEndpointReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			Audit:  recorder},
		&controllers.InferenceLeaseReconciler{
			Client:               mgr.GetClient(),
			Scheme:               mgr.GetScheme(),
			RuntimeImage:         env("SOVEREIGN_VLLM_IMAGE", "vllm/vllm-openai:v0.10.2"),
			DefaultStaticVRAMMiB: int64(envInt("SOVEREIGN_MODEL_VRAM_MIB", 4096)),
			DefaultMaxKVRAMMiB:   int64(envInt("SOVEREIGN_KV_VRAM_MIB", 8192)),
			SafetyHeadroomMiB:    int64(envInt("SOVEREIGN_VRAM_HEADROOM_MIB", 1024)),
			Policy:               policyEvaluator,
			Audit:                recorder,
		},
	}
	for _, reconciler := range reconcilers {
		if err := reconciler.SetupWithManager(mgr); err != nil {
			log.Fatalf("unable to register controller: %v", err)
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Fatalf("unable to configure health check: %v", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Fatalf("unable to configure readiness check: %v", err)
	}

	// Block execution stream into the informer pipeline
	log.Println("Sovereign AI Controller Loop initialized. Starting manager event watch...")
	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("fatal crash inside active manager loop: %v", err)
	}

	log.Println("Controller cleanly terminated.")
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationPtr(value time.Duration) *time.Duration { return &value }

func newLogger() logr.Logger {
	opts := []zap.Opts{zap.UseDevMode(envBool("SOVEREIGN_DEV_LOGGING", false))}
	if value := os.Getenv("SOVEREIGN_LOG_VERBOSITY"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			opts = append(opts, zap.Level(zapcore.Level(-parsed)))
		}
	}
	return zap.New(opts...)
}
