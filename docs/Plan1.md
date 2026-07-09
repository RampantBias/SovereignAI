SovereignAI MVP 1 Implementation Plan
Summary
Rebuild the current prototype into a tested Kubernetes control plane capable of executing the canonical software-development workflow:
initialize → branch → architect → test writer → developer → agent review → tests → human gate/intervention → commit/push → build/verification → Argo CD/Kustomize preview → product approval → merge
MVP 1 proves reliability, security, auditability, human control, inference sharing, and recovery. It remains sequential and uses direct vLLM; dynamic graphs, parallel steps, external CI, llm-d, cloud providers, and advanced analytics remain post-MVP.
Deliver the work in gated increments. Each increment must compile, pass its tests, and leave the repository runnable before the next subsystem is added.
Public APIs and Architecture
Kubernetes resources
Redesign the unstable v1alpha1 API without backward-compatibility migration:
SovereignProject — cluster-scoped repository references, validation configuration, policy profile, retention, and credential references.
SovereignWorkflow — lives inside its isolated execution namespace; holds immutable sequential intent and concise aggregate status.
StepAttempt — one immutable execution attempt, including execution kind, inputs, capabilities, timeout, pod/job reference, result, and failure reason.
Artifact — immutable metadata for a content-addressed PVC object: contract version, digest, producer, classification, path, and validation state.
HumanSession — requester identity, TTL, tool profile, capabilities, workspace revisions, connection state, and terminal outcome.
ValidationRun — provider, candidate commit/image digest, Kustomize configuration, deployment status, URL, and structured result.
InferenceEndpoint — resides in sovereign-inference; describes model/runtime identity, security domain, static capacity, health, Kubernetes placement, and active lease summary.
InferenceLease — workflow-scoped request for model access, estimated KV budget, priority, classification, sharing scope, endpoint reference, and lifecycle.
PolicyProfile — cluster-scoped reference to a versioned OPA bundle and its configuration.
Use typed phases, Kubernetes conditions, observed generations, schema validation/defaulting, CEL immutability where appropriate, consistent labels, deterministic names, and finalizers. Regenerate deep-copy code and installable CRD manifests with controller-gen.
Interfaces
Introduce narrow internal contracts:
PolicyEvaluator
AuditRecorder
InferenceProvider
ValidationProvider
ContextProvider
UtilityExecutor
ArtifactRegistrar
IdentityAuthorizer
Implementations for MVP 1:
Embedded OPA SDK with versioned Rego bundles.
PostgreSQL audit service.
Direct vLLM inference provider.
Argo CD/Kustomize validation provider.
Read-only repository MCP/context service.
Kubernetes Job-based deterministic utility executor.
Keycloak OIDC authorization.
Istio sidecar-mode workload identity and mTLS.
Information boundaries
Maintain four distinct sources:
CRDs: current desired and observed state.
PostgreSQL events: append-only execution history and decision evidence.
Prometheus: logs/metrics/traces and DCGM/vLLM measurements.
PVC artifacts: versioned inputs, outputs, context bundles, patches, and reports.
Decision lineage and CLI playback are projections over these sources, not additional stores.
Implementation Sequence
1. Stabilize the repository
Fix current vet/build failures, invalid phase constants, broken storage existence checks, ignored errors, label/selector mismatches, API port inconsistencies, and Dockerfile paths.
Remove committed/generated executables from source control and add appropriate ignore rules.
Separate NVIDIA-specific compilation from portable builds. Retire the custom NVML metrics loop in favor of DCGM Exporter and vLLM metrics; keep no mandatory NVML dependency in normal unit-test builds.
Introduce repeatable make generate, make manifests, make test, make test-integration, and image-build targets.
Add manager health/readiness endpoints, structured logging, configuration loading, leader election, graceful shutdown, and pinned image/config values.
Quarantine or remove obsolete imperative internal/kube code after confirming no live callers.
Replace the invalid example manifest with a schema-valid minimal workflow.
Gate: go test ./..., vet, generated-code verification, and image builds pass on a non-GPU development machine.
2. Establish domain state machines and CRDs
Implement pure transition functions for Workflow, StepAttempt, HumanSession, ValidationRun, InferenceLease, and InferenceEndpoint.
Make retries create new attempts; never overwrite failed attempt history.
Define terminal reasons, retryability, timeouts, cancellation, intervention, and invalid-contract behavior.
Add SovereignProject and PolicyProfile; change project creation to create/reconcile project intent instead of directly invoking Argo.
Have the API validate Project and workflow intent, run OPA admission, create the isolated namespace, then create the Workflow inside it. Roll back the namespace if Workflow creation fails.
Delete workflows through namespace deletion. Workflow finalization cleans external Argo and inference references before allowing namespace termination.
Keep detailed child state out of Workflow status; store only active step/attempt, aggregate phase, conditions, and references.
Gate: envtest proves creation, progression, status conflicts, idempotency, retry creation, cancellation, and namespace/finalizer cleanup.
3. Add audit and policy foundations early
Build an internal audit ingestion/query service backed by PostgreSQL.
Use deterministic event IDs and idempotent inserts so producer retries cannot duplicate history.
Emit events for admission, state transitions, OPA decisions, capabilities, identities, artifacts, inference leases, utility operations, validation, human sessions, and failures.
Include workflow/step revisions, agent image digest, model/runtime revision, context bundle, contracts, tools, artifact digests, endpoint/hardware identity, causation IDs, and telemetry correlation windows.
Add CLI timeline playback ordered by workflow and causation.
Embed OPA in API/controller processes and load versioned bundles from controlled configuration.
Implement only these MVP policies:allowed agent images, registries, models, and tools;
workflow/resource bounds;
classification/tenant compatibility for inference sharing;
lease priority and eviction eligibility;
required human approvals and validation before commit/merge;
permitted Git and validation transitions.

Fail closed for privileged Git transitions, identity authorization, or unavailable policy. Ordinary reconciliation may retry when audit persistence is transiently unavailable, but it must not silently lose required events.
Gate: policy tests provide allow/deny explanations, audit retries are idempotent, and a failed attempt can be reconstructed through CLI playback.
4. Refactor reconciliation into focused controllers
Run the reconcilers in one controller-manager binary initially:
Workflow controller creates only the next required child intent and computes aggregate state.
StepAttempt controller dispatches agent and deterministic utility jobs.
Artifact controller validates immutable metadata and accepted digests.
HumanSession controller manages snapshots, code-server pods, services, identities, TTL, and closure.
ValidationRun controller delegates to ValidationProvider.
InferenceEndpoint controller manages shared vLLM lifecycle and idle retention.
InferenceLease controller performs OPA-backed compatibility, capacity, priority, and eviction decisions.
All reconcilers must use create-or-observe behavior, optimistic concurrency, stable field indexes, owned-resource watches, explicit requeues, and conditions. Remove direct cross-domain operations from the monolithic Workflow controller.
Gate: deleting/restarting the controller at every major transition produces no duplicate pods, jobs, artifacts, commits, leases, or validation environments.
5. Implement governed agent execution and artifacts
Change the Go wrapper into a real supervisor for an arbitrary configured executable.
Version input.json and result.json; validate both against committed schemas.
Give each attempt its own control and staging paths.
Disable service-account token automount for agent containers.
Forward termination with a bounded grace period and record exit, timeout, interruption, and malformed-result outcomes.
After agent completion, run a trusted collector Job that mounts the PVC, validates results, hashes outputs, copies accepted content to a root-owned content-addressed artifact directory, and creates Artifact CRs.
Mount accepted prior artifacts read-only into subsequent attempts.
Build a small first-party reference agent:role profiles for architect, test writer, developer, and reviewer;
OpenAI-compatible vLLM client;
read-only MCP repository/context access;
constrained writes to declared workspace/staging paths;
no Git credentials or general-purpose shell.

Use a fake OpenAI-compatible server in tests.
Gate: all four role profiles complete contract fixtures, invalid outputs fail safely, and artifacts retain schema, digest, producer, and source revision.
6. Implement MCP/context and deterministic utility jobs
Build an MCP server exposing only:repository tree listing;
exact bounded file reads;
bounded text search;
repository metadata;
immutable context-bundle creation.

Authenticate attempt identities through Istio workload identity and authorize every operation against the active attempt and OPA capability decision.
Structure the MCP tool registry and authorization middleware so filesystem and action tools can be added later without changing transport or audit semantics.
Implement utility Jobs for:repository initialization;
branch creation;
tests;
candidate build producing an immutable internal-registry image digest;
commit and push;
merge.

Use standard Git CLI behavior with short-lived, operation-scoped Secrets mounted only into Git Jobs.
Require idempotency keys and inspect repository state before retrying ambiguous Git operations.
Let each Project configure test/build job images and commands; the platform enforces limits, identity, network policy, result contracts, and artifact collection.
Gate: agent pods cannot access Git credentials; repeated utility reconciliation cannot create duplicate commits or merges.
7. Implement inference multiplexing and policy
Run one vLLM process per Kubernetes-assigned GPU or MIG device; do not use NVIDIA multi-process time-slicing in MVP 1.
Let Kubernetes place vLLM pods through normal GPU resources, affinity, topology, and device-plugin behavior. Do not set spec.nodeName or CUDA_VISIBLE_DEVICES from the workflow controller.
Implement custom SovereignAI scheduling above Kubernetes:reuse a compatible warm endpoint first;
evaluate tenant, project, classification, model revision, adapter, and sharing scope through OPA;
reserve static model memory once per endpoint;
reserve estimated KV budget per lease;
retain configurable safety headroom;
wait/reject when safe capacity is unavailable;
evict lower-priority eligible leases when policy permits;
terminate affected attempts, release their leases, and retry them from the prior accepted step boundary.

Use vLLM continuous batching for concurrent agent requests.
Use DCGM Exporter for hardware metrics and vLLM metrics for queue, cache, token, and latency behavior. CRDs retain summarized health and reservations, not high-frequency samples.
Create Prometheus recording rules and Grafana dashboards for endpoint occupancy, lease allocation, GPU/VRAM use, workflow duration, retry rate, intervention rate, and inference performance.
Gate: two compatible agents share one endpoint; incompatible classifications do not; unsafe capacity is rejected; higher-priority admission produces an explainable eviction decision.
8. Implement identity, mesh, and human sessions
Deploy Keycloak for air-gapped OIDC and use device authorization flow in the CLI.
Add TLS and JWT-validation gRPC interceptors to the API; authorize project/workflow actions from subject and group claims.
Deploy Istio sidecars with strict mTLS and explicit AuthorizationPolicies for API, MCP, audit, inference, validation, and human-session traffic.
Human intervention:interrupts the active autonomous attempt;
snapshots the accepted workspace revision;
creates a dedicated HumanSession service account and code-server pod;
exposes it through an OIDC-authenticated session gateway;
enforces TTL and workflow-scoped access;
records connection lifecycle, capability grants, and before/after workspace/artifact digests;
resumes through a new agent attempt or approval transition.

Do not impersonate the agent identity or record keystrokes/screen content in MVP 1.
Gate: an authorized user can enter only the requested workflow; another user/project is denied; expiry revokes access; resulting changes are attributable to the human session.
9. Implement validation and canonical workflow
Replace CDClient with ValidationProvider.
Implement the two-repository Argo CD/Kustomize provider:Project references application and infrastructure repositories;
Project validation config identifies the Kustomize overlay;
deterministic test/build jobs produce a commit and immutable image digest;
ValidationRun applies the image digest to the configured overlay;
provider creates an Argo Application targeting the workflow’s isolated validation destination;
controller observes sync/health, reports a review URL, captures structured results, and cleans up.

Do not retain the branch-wide ApplicationSet as the universal validation abstraction.
Implement the canonical stage sequence and require product approval before merge.
Represent internal test/build jobs as the MVP CI-equivalent while preserving a future external CI provider interface.
Gate: the complete workflow changes SovereignAI code, produces a preview, records product approval, and merges only after all policy gates succeed.
10. Complete API, CLI, packaging, and operations
Regenerate gRPC APIs around Project, Workflow submission/status/watch, approval, intervention, HumanSession access, cancellation/cleanup, and audit playback.
Make commands non-interactive where automation matters; keep an optional shell as a thin wrapper.
Package controller, API, audit service, MCP service, reference agent, collector/utility runner, and manifests.
Supply Kustomize overlays for development and air-gapped MVP deployment, including Keycloak, PostgreSQL, Istio policies, Prometheus/Grafana, Argo CD integration, and DCGM dependencies.
Pin all images and support internal mirrors and preloaded models.
Update the roadmap so MVP 1 contains structured audit playback; MVP 2 contains advanced retention/integrity, rich playback UI, and comparative workflow analytics. Correct “January 2026” to January 2027 if that milestone follows September 2026.
Test and Acceptance Plan
Automated layers
Unit tests: state machines, naming, schema validation, artifact hashing, capacity arithmetic, sharing compatibility, priority eviction, retry classification, OPA rules, and event causation.
Contract tests: agent input/result, artifacts, audit events, MCP tools, Project validation configuration, and protobuf compatibility.
Envtest: every reconciler, ownership/finalizers, status conflicts, stale-cache behavior, watches/indexes, deletion, restart reconstruction, and duplicate prevention.
Component tests:PostgreSQL audit persistence and playback;
Keycloak/JWT validation with local JWKS fixtures;
MCP authorization and path traversal/size limits;
fake vLLM health/OpenAI endpoints;
fake Argo dynamic resources;
Git utility idempotency against a temporary bare repository.

Non-GPU integration cluster: install all CRDs/controllers with fake inference and exercise the complete workflow.
Hardware E2E: run on the multi-node NVIDIA k3s environment with vLLM, DCGM, Istio, Argo CD, Keycloak, and PostgreSQL.
Security tests: denied images/tools/models, cross-namespace access, invalid JWTs, expired sessions, unavailable policy, secret absence from agent pods, and network-policy enforcement.
Chaos tests: controller deletion, vLLM deletion, lease loss, Job deletion, API conflict, audit outage, ambiguous Git push, Argo failure, and HumanSession disconnect.
Quality gates: race detector where supported, vet/static analysis, generated-file verification, container build, manifest validation, and vulnerability/SBOM checks.
MVP acceptance demonstration
MVP 1 is complete only when:
An OIDC-authenticated developer creates a Project and submits the canonical workflow.
Every workflow receives an isolated namespace, PVC, identities, policies, and child resources.
Multiple compatible agent attempts share one vLLM endpoint with separately accounted KV leases.
OPA rejects incompatible classification sharing and explains the decision.
Agents use read-only MCP context and cannot access Git credentials.
Typed, immutable artifacts move between all stages.
A human can approve or open an authenticated, time-limited code-server session and produce an attributable workspace revision.
Deterministic Jobs test, build, commit, push, and merge without duplicate side effects.
Argo CD/Kustomize deploys the commit-built immutable image into an ephemeral review environment.
Killing vLLM during an attempt records the failure, interrupts the attempt, reacquires capacity, retries from the prior accepted boundary, and succeeds.
Restarting the controller does not duplicate accepted work.
CLI playback reconstructs the execution with links to artifacts and GPU/inference telemetry.
Grafana shows GPU, inference, workflow, retry, and intervention metrics.
The repository installs reproducibly on the documented air-gapped-capable k3s/NVIDIA environment.
Assumptions
Target milestone is September 2026; the roadmap’s January 2026 MVP 2 date is assumed to mean January 2027.
v1alpha1 is pre-public and may be replaced without migration compatibility.
Advanced comparative analytics are deferred, but MVP events and metric labels preserve the data needed for them.
Direct vLLM is the only inference provider implemented in MVP 1; llm-d remains a later provider.
Physical GPU placement remains Kubernetes-native; SovereignAI owns endpoint reuse, KV admission, classification, priority, and eviction.
The full CRD resource model, Keycloak, Istio, embedded OPA, PostgreSQL, and structured playback are mandatory MVP 1 scope.
The implementation does not add dynamic workflow mutation, parallel steps, external CI providers, cloud models, mid-step checkpoints, full human-session recording, or NVIDIA multi-process GPU time-slicing.