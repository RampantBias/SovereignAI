# SovereignAI Reduced MVP Demonstration Contract

Status: **Frozen for implementation**

Version: **v3**

Revised: **2026-07-19**

Implementation guide: [draft/Plan3.md](../draft/Plan3.md)

## Demonstration objective

Demonstrate that SovereignAI can execute a useful, bounded software-development workflow against an independent application repository with credible reliability, security, human control, deterministic side effects, and auditability.

The MVP is a vertical slice and public architectural demonstration. It is not a production release. It proves that the control plane can preserve authority and evidence across autonomous work, deterministic Git and build operations, local inference, validation, human approval, recovery, and merge.

The demonstration workload is a separate Go HTTP calculator application. SovereignAI does not modify its own controller repository during the MVP.

## Frozen demonstration workload

The reference calculator repository contains:

- a Go HTTP service;
- `GET /healthz`;
- a versioned JSON calculation endpoint;
- baseline support for add, subtract, and multiply;
- unit tests;
- a Dockerfile that produces one application image; and
- a Kustomize overlay suitable for ephemeral Argo CD validation.

The submitted change request is fixed:

> Add divide support to the calculator without regressing existing operations. `84 / 2` must return `42`. Division by zero must return HTTP 422 with the stable error code `division_by_zero`.

The reference repository is hosted on an existing HTTPS Git remote. Git hosting is not installed by SovereignAI. The remote must be reachable from utility and Argo CD workloads, and repository credentials must be supplied through a Project-owned Kubernetes Secret. The calculator seed procedure must refuse to overwrite or initialize a remote that already contains a branch.

## Primary user and environment

- User: an application developer who submits the workflow and a maintainer who reviews the validated result.
- Application repository: the external calculator Git repository.
- Cluster: self-managed, air-gap-capable k3s/Kubernetes.
- Primary inference hardware: the NVIDIA 5060 Ti 16 GB node.
- Non-gating hardware: the NVIDIA 1070 8 GB node. The MVP does not require it to serve a model or pass the demonstration.
- Inference: one pinned vLLM image, one pinned model revision, one measured runtime profile, and policy-controlled warm endpoint reuse.
- Validation: one Argo CD/Kustomize provider that deploys the exact calculator image digest.
- Audit: one shared PostgreSQL audit store used by the API, controllers, collectors, and runtime boundaries.

### Host prerequisites

The operator supplies and configures:

- k3s/Kubernetes and containerd;
- the compatible NVIDIA host driver and NVIDIA container runtime;
- a default or explicitly selected storage class;
- network reachability to the existing HTTPS Git remote; and
- any host-level registry trust or mirror configuration required by k3s/containerd.

SovereignAI preflight checks these prerequisites but does not install or modify host drivers, the container runtime, or Kubernetes itself.

### Bundled cluster services

The demo distribution installs pinned, digest-addressed cluster resources for:

- Argo CD;
- PostgreSQL for audit storage;
- a local OCI registry;
- Prometheus;
- the NVIDIA device plugin;
- the model-cache PVC and controlled preload/verification jobs; and
- SovereignAI CRDs, control-plane workloads, policies, and demo resources.

Every third-party image and manifest revision must be recorded in the demo inventory. Air-gap packaging transfers the required images and manifests without embedding credentials, model access tokens, TLS private keys, or human JWTs in the repository.

## Canonical sequential workflow

The workflow graph is fixed and sequential. Repository initialization remains an explicit stage; feature-branch creation is folded into candidate preparation.

| Order | Stage | Kind | Required output or decision |
| --- | --- | --- | --- |
| 1 | Initialize repository | Utility: `repository.initialize` | accepted `repository-revision/v1` containing the immutable source commit |
| 2 | Architect | AgentRun | accepted `implementation-plan/v1` |
| 3 | Developer | AgentRun | accepted unified-diff `change-set/v1` |
| 4 | Prepare candidate | Utility: `candidate.prepare` | accepted `prepared-candidate/v1` containing deterministic branch, base commit, change-set digest, and Git tree |
| 5 | Test candidate | Utility: `test.run` | accepted `test-report/v1` bound to the prepared Git tree |
| 6 | Commit candidate | Utility: `git.commit` | accepted `candidate-revision/v1` bound to the tested tree |
| 7 | Push candidate | Utility: `git.push` | evidence that the remote candidate branch resolves to the exact candidate commit |
| 8 | Build calculator | Utility: `build.image` | accepted `image-digest/v1` for one calculator image built from the candidate commit |
| 9 | Ephemeral validation | ValidationRun | accepted `validation-result/v1`, Argo health, and direct review instructions |
| 10 | Product approval | HumanGate | admitted ApprovalDecision for the exact validated subject |
| 11 | Merge candidate | Utility: `git.merge` | accepted `merge-revision/v1` proving the approved candidate was merged and pushed once |

The submitted change request is an immutable workflow input. Every later input is resolved to an accepted Artifact by API version, kind, namespace, name, UID, digest, contract, producer, workflow UID, classification, and source commit before its consumer resource is created.

## Required capabilities

### Workflow lifecycle and Project authority

- Submit one predefined, schema-valid workflow through the authenticated CLI.
- Require the exact referenced SovereignProject UID to be current and `Ready=True` before admitting the workflow.
- Create one isolated namespace and PVC-backed workspace per workflow.
- Validate the complete graph, operation vocabulary, contracts, capabilities, timeouts, model rules, and resource bounds before namespace creation.
- Advance steps idempotently through Kubernetes reconciliation.
- Represent every retry as a new StepAttempt and retain prior attempt history.
- Enforce explicit TTL and cleanup behavior.

### Agent execution and context

- Use exactly two short-lived agent roles: Architect and Developer.
- Supervise the repository-owned Go reference agent through the existing Go wrapper and versioned AgentContract.
- Run a local MCP context server as a sidecar in each agent Pod.
- Mount the admitted repository read-only into the MCP sidecar, not into the agent container.
- Expose MCP only on localhost and authorize its bounded methods with a short-lived AgentRun capability JWT.
- Give the agent only its attempt-specific staging directory as writable storage.
- Give agent and MCP containers no Kubernetes service-account token and no Git or registry credential.
- Validate `result.json`, all declared artifacts, and every required output obligation before accepting the producer.

The Architect consumes the immutable change request and bounded repository context and produces `implementation-plan/v1`. The Developer consumes that exact change request and accepted implementation plan, retrieves bounded context through MCP, and produces `change-set/v1` as a unified diff based on the admitted source commit.

### Artifact contracts and provenance

- Define versioned Go types with runtime validation for every MVP Artifact contract.
- Commit matching JSON Schemas for interoperability and documentation; Go validation remains the runtime authority.
- Store Artifact content by digest in the workflow artifact store.
- Recompute and verify the content digest before a consumer can use an Artifact.
- Mount resolved input content read-only.
- Reject the wrong UID, digest, contract, workflow, producer, classification, or source commit.
- Keep repository source commit and workflow-definition revision as separate provenance facts.
- Block producer success until all required Artifact resources report accepted status.
- Never expose failed-attempt staging content to a later attempt.

### Deterministic candidate, Git, and build operations

- `candidate.prepare` verifies a clean repository at the accepted source commit, creates a deterministic candidate branch, validates the unified diff, applies it with `git apply --check`, stages it, and records the exact Git tree.
- `test.run` executes the Project-owned test command against that exact prepared tree and records bounded result evidence.
- `git.commit` refuses to commit a tree other than the accepted tested tree.
- `git.push` proves the remote candidate branch resolves to the exact candidate commit.
- `build.image` builds only from that commit and records one immutable calculator image digest.
- Utility credentials are issued only to the operation that requires them and are deleted before collection succeeds.
- Retries converge on the same accepted effect or fail closed when an idempotency identity is reused for different inputs.

### Validation and review access

- ValidationRun contains the exact candidate commit, image digest, pinned infrastructure revision, overlay, destination, timeout, and teardown policy.
- The Argo provider creates one deterministic Application per ValidationRun.
- Success requires Argo to observe the intended source and image digest and report `Synced` and `Healthy`.
- Validation produces `validation-result/v1` and documented port-forward instructions.
- The review path exposes `/healthz` and the calculation endpoint from the ephemeral calculator deployment.
- Validation teardown is idempotent, and its finalizer remains until Application deletion is observed.

### Human identity and approval

- Serve the gRPC API over TLS.
- Authenticate human CLI calls with short-lived Ed25519-signed JWTs.
- Validate issuer, audience, signature, subject, groups, issued time, and expiry.
- Derive ApprovalDecision subject and groups from the verified token; never trust caller-supplied identity fields.
- Support one `AnyOf` rule requiring membership in `maintainers`.
- Bind the ApprovalRequest and ApprovalDecision to the candidate commit, candidate tree, calculator image digest, ValidationRun UID, and validation-result digest.
- Support explicit approve and deny CLI commands.
- Treat denial as terminal for the MVP.

HumanSession and interactive workspace editing are not part of the reduced MVP.

### Inference and recovery

- Run one pinned local vLLM model profile on the 5060 Ti node.
- Reuse the compatible warm endpoint between the Architect and Developer when policy permits.
- Reject or wait when safe capacity is unavailable.
- Bound image-pull, scheduling, startup, model-load, readiness, and request failures.
- Release lease accounting on deletion, interruption, or endpoint loss.
- Detect loss of the endpoint bound to a running AgentRun.
- Retry only at the StepAttempt boundary; the agent client does not perform hidden retries.

### Security and audit

- Use separate identities for API, controller, agent, MCP, utility, collector, validation, and human actions.
- Use Ed25519 key pairs with separate issuers for human and workload identities.
- Never commit private keys or JWTs.
- Apply deny-by-default NetworkPolicies and allow only required DNS, inference, audit, registry, Git, Argo, and control-plane traffic for the relevant workload.
- Keep Git credentials out of agent and MCP containers.
- Persist structured audit events to PostgreSQL rather than ordinary logs or process-local memory.
- Render a human-readable workflow timeline.
- Expose minimum workflow, attempt, inference lease, endpoint, retry, and validation metrics.

## Primary failure demonstration

The controlled failure occurs during the first Developer attempt:

1. Architect completes and its implementation plan becomes an accepted Artifact.
2. Developer starts with a lease bound to the warm vLLM endpoint.
3. The operator deletes the active vLLM Pod using the documented fault-injection command.
4. SovereignAI observes terminal endpoint loss, interrupts the bound lease, releases accounting once, and prevents further endpoint reuse.
5. AgentRun terminates the stale Developer Pod.
6. StepAttempt becomes retryably interrupted.
7. Workflow creates exactly one replacement Developer attempt and lease.
8. The replacement consumes the same accepted change request and implementation-plan UID/digest bindings.
9. Developer succeeds, and the workflow continues without accepting failed-attempt content or duplicating a later side effect.

The timeline must show the endpoint, lease, failed attempt, selected recovery policy, replacement attempt, preserved Artifact boundary, and final outcome without requiring log interpretation.

A controller restart remains a rehearsed secondary scenario. It is not a substitute for the endpoint-loss demonstration.

## Acceptance criteria

The demonstration succeeds only when all of the following are true:

1. A clean installation deploys the bundled cluster services and SovereignAI after host preflight passes.
2. An authenticated developer submits the canonical calculator workflow through the TLS-protected CLI.
3. The exact Project UID is Ready, and all workflow workloads run inside the intended namespace and identity boundary.
4. Architect and Developer use the pinned local model; compatible warm reuse is visible before fault injection.
5. Agents receive only declared capabilities, use the authenticated localhost MCP sidecar, and receive no Git credential, registry credential, or Kubernetes token.
6. Accepted Artifacts advance through the workflow by immutable identity and digest rather than shared-workspace convention.
7. The tested Git tree, candidate commit, pushed branch, calculator image digest, ValidationRun, ApprovalDecision, and merge evidence form one consistent chain.
8. The ephemeral environment is Healthy, `/healthz` succeeds, `84 / 2` returns `42`, division by zero returns HTTP 422 with `division_by_zero`, and existing operations still pass.
9. Only an authenticated member of `maintainers` can approve the exact validated subject; stale, mismatched, unauthorized, duplicate, and denied decisions behave deterministically.
10. The controlled endpoint deletion produces one interrupted attempt, correct release, one replacement attempt, and eventual success from the prior accepted boundary.
11. Merge pushes exactly the candidate that was tested, built, validated, and approved.
12. Cleanup leaves no validation Application, inference allocation, operation credential, workload, or orphan workflow namespace outside the configured retention policy.
13. The PostgreSQL-backed timeline explains actors, authority primitives, policy decisions, Artifact identities, side effects, recovery, human decision, merge, and cleanup.

## Explicit non-goals

- modifying SovereignAI's own controller repository during the demo;
- dynamic or agent-created workflow graphs;
- parallel steps;
- test-writer or reviewer agent roles;
- an early engineering approval gate;
- HumanSession or interactive workspace editing;
- an external CI provider beyond the deterministic Project-owned test operation;
- cloud inference providers;
- multiple models, quantizations, routing strategies, or inference providers;
- requiring the 1070 GPU to serve inference;
- generalized validation-provider discovery;
- generalized multi-cluster management;
- a service mesh or external identity provider;
- installing k3s, host GPU drivers, or the NVIDIA container runtime;
- bundling a Git service;
- transparent mid-token recovery, live migration, or sophisticated inference preemption;
- arbitrary unreviewed custom-agent images;
- comparative agent-performance claims; and
- production-grade regulated-industry certification.

## Frozen implementation decisions

- `draft/Plan3.md` is the sole active implementation guide.
- The live demonstration ends in merge, not merely an approved candidate.
- Repository initialization remains a visible workflow stage.
- Candidate branch creation belongs to `candidate.prepare`.
- The calculator repository is external and uses HTTPS Git credentials.
- The calculator produces one candidate image digest.
- The reference agent is a small Go executable with Architect and Developer modes.
- Repository context is served by an authenticated MCP sidecar in the Agent Pod.
- Artifact runtime authority is implemented with typed Go contracts and mirrored JSON Schemas.
- Approval uses one `AnyOf` maintainers rule.
- Human and workload authentication use separate Ed25519 JWT issuers over TLS.
- Supporting cluster services are bundled; host and Git infrastructure are prerequisites.
