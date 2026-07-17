# Architecture overview

## Purpose

SovereignAI is a Kubernetes control plane for governed autonomous execution. It converts a declarative workflow into isolated, observable execution while coordinating agents, local inference, deterministic operations, human participation, validation, and recovery.

This document describes the target architecture.

## Scope and boundaries

SovereignAI owns:

- workflow and step lifecycle;
- execution isolation and workspace lifecycle;
- capability admission and workload identity;
- artifact contracts and handoff metadata;
- inference requests and sharing policy;
- human gates and intervention sessions;
- deterministic utility (i.e. GIT) operations;
- validation-provider coordination;
- recovery policy and authoritative audit events.

SovereignAI does not own:

- an agent's internal reasoning implementation;
- model implementation or training;
- Kubernetes device-driver implementation;
- general-purpose source control or CI/CD;
- every form of application validation; or
- a universal context-engine solution.

## Conceptual architecture

```text
                           +----------------------+
 Developer / Reviewer --->| CLI / future UI      |
                           +----------+-----------+
                                      |
                                      v
                           +----------------------+
                           |  Control-plane API   |
                           +----------+-----------+
                                      |
                         Kubernetes custom resources
                                      |
                                      v
 +----------------+       +----------------------+       +----------------+
 | Audit          |<------| Workflow controllers |------>| Policy engine  |
 | domain events  |       | desired vs observed  |       | admission      |
 +----------------+       +--+----+----+----+----+       +----------------+
                            |    |    |    |
             +--------------+    |    |    +----------------+
             v                   v    v                     v
 +-------------------+ +-----------+ +---------------+ +------------------+
 | Execution plane   | | Human     | | Utility plane | | Validation       |
 | agent attempts    | | sessions  | | Git and other | | provider         |
 +---------+---------+ +-----+-----+ +-------+-------+ +------------------+
           |                 |               |
           +-----------------+---------------+
                             |
                      shared workflow PVC
                             |
                             v
                   +---------------------+
                   | Artifact contracts  |
                   | and provenance      |
                   +---------------------+

 +-------------------+    policy/request     +---------------------------+
 | Workflow step     |---------------------->| Inference provider        |
 +-------------------+                       | direct vLLM / llm-d later |
                                             +-------------+-------------+
                                                           |
                                              Kubernetes GPU allocation
```

## Primary resources

### Project

A logical grouping for workflows, repositories, policy defaults, validation configuration, and retention. A project is not the execution-isolation boundary.

**AUTHOR NOTE:** Decide whether `Project` remains a Kubernetes CRD or is represented by configuration and labels managed through the API.

### Workflow

A declarative execution request. Every workflow receives its own Kubernetes namespace and workspace. For the MVP, its graph is predefined, immutable after admission, sequential, and represented by a `SovereignWorkflow` CRD.

The workflow CRD is the MVP source of truth for desired state and summarized execution status. Large artifacts, logs, and detailed audit payloads do not belong in CRD status.
CRD status represents the latest observable state needed for reconciliation and operators. The append-only event history records how the resource reached that state. Metrics and traces explain its operational behavior.

### Step

A declared unit in a workflow. A step describes its inputs, expected outputs, execution kind, capabilities, model requirements, validation requirements, and transition policy. A step is not synonymous with a single pod.

Initial execution kinds are:

- `Agent`: supervised autonomous execution;
- `Utility`: deterministic platform-controlled operation;
- `HumanGate`: explicit approval or rejection; and
- `Validation`: provider-specific evaluation of produced changes.

### StepAttempt

One workflow-lifecycle attempt of a step. Retries create new attempts rather than overwriting the history of a failed attempt. `StepAttempt` is an envelope: it records workflow identity, attempt number, execution kind, a typed reference to the owned domain primitive, and the outcome mirrored from that primitive. It does not contain agent commands, utility operations, approval rules, validation subjects, pods, Jobs, credentials, or provider state.

The execution reference identifies exactly one of `AgentRun`, `UtilityOperation`, `ApprovalRequest`, or `ValidationRun`. Those resources are immutable desired-state primitives with independent status ownership.

### AgentRun

An immutable delegation of bounded autonomous work. It owns agent responsibility, runtime, capabilities, inference requirements, the agent pod, inference lease, result collection, and agent-specific failure state.

### UtilityOperation

An immutable grant to perform one named deterministic platform operation. It owns policy admission, Project-derived execution inputs, scoped credentials, the utility Job, idempotency identity, collection, and operation-specific failure state.

### ApprovalRequest

An immutable request for an attributable human decision. It owns approval requirements and, once B2 is complete, observes immutable `ApprovalDecision` resources. It remains `AwaitingApproval` rather than allowing clients to patch a StepAttempt outcome.

### Artifact

A typed, immutable handoff produced by an attempt. Artifact metadata records schema, path or object reference, digest, producer, creation time, and consumers. The initial physical storage is the workflow PVC; the API contract should not permanently depend on local paths.

### HumanSession

A time-limited, separately identified interactive environment that mounts the workflow workspace. It is created only after API authorization and never assumes the agent's identity.

### ValidationRun

A provider-neutral request to validate a workflow revision. Argo CD is the MVP provider for deploying an ephemeral environment, but desktop, database, frontend, and other validations may use different providers later.

### InferenceLease

A policy-approved association between a step attempt and an inference endpoint. It captures model identity, tenant/security domain, endpoint, capacity assumptions, and lifecycle. The provider may reuse an existing endpoint or create one.

## Reconciliation boundaries

The workflow controller should remain high-level. It advances the workflow state machine and creates subordinate intent. It should not directly contain every implementation detail.

Target boundaries are:

- `WorkflowReconciler`: workflow graph and terminal state;
- `StepAttemptReconciler`: workflow-attempt lifecycle and domain-outcome projection only;
- `AgentRunReconciler`: autonomous runtime, inference lease, agent pod, and agent evidence;
- `UtilityOperationReconciler`: deterministic policy admission, scoped authority, utility Job, and utility evidence;
- `ApprovalRequestReconciler`: human decision request and decision aggregation;
- `HumanSessionReconciler`: interactive workspace sessions;
- `ValidationReconciler`: provider invocation and result collection;
- `InferenceProvider`: endpoint acquisition and release;
- Kubernetes scheduler/DRA: physical device placement;
- `AuditRecorder`: durable domain-event emission.

The MVP may combine some controllers for delivery speed, but their domain boundaries should remain visible in types and interfaces.

## Provider interfaces

The architecture deliberately places varying infrastructure behind narrow interfaces:

```go
type InferenceProvider interface {
    Acquire(ctx context.Context, request InferenceRequest) (InferenceLease, error)
    Release(ctx context.Context, lease InferenceLease) error
    Health(ctx context.Context, lease InferenceLease) (HealthStatus, error)
}

type ValidationProvider interface {
    Start(ctx context.Context, request ValidationRequest) (ValidationRun, error)
    Status(ctx context.Context, run ValidationRun) (ValidationStatus, error)
    Destroy(ctx context.Context, run ValidationRun) error
}
```

These signatures are illustrative, not committed Go APIs.

## State and ownership

- Kubernetes custom resources hold desired state and concise observed state.
- Namespaced resources are owned by namespaced workflow resources where Kubernetes ownership rules permit it.
- The workflow namespace is cluster-scoped and therefore cannot use a namespaced workflow as its owner. Namespace cleanup requires an explicit finalizer/controller strategy or a cluster-scoped owner.
- The workspace persists across step attempts and human sessions for the workflow lifetime.
- Artifacts are immutable once accepted; new work produces new revisions.
- Audit events are append-only and external to ordinary controller logs.

SovereignAI keeps four information sources distinct: Kubernetes current state, append-only audit events, operational telemetry, and versioned artifacts. Decision lineage, execution playback, and comparative workflow evaluation are derived by correlating those sources; they are not additional control-plane state stores. See [Audit model](audit-model.md).

## Current implementation mapping

| Current area | Target direction |
| --- | --- |
| `SovereignWorkflowController` | High-level workflow reconciliation |
| `GPUNode` and embedded placement | Move physical allocation toward Kubernetes scheduling/DRA |
| direct vLLM builders | First `InferenceProvider` implementation |
| `internal/argo` | Argo-backed `ValidationProvider` that uses Kustomize to reconcile revisions |
| `agent-wrapper` | Supervisor for arbitrary contract-compliant agent executables |
| `buildHumanPod` | `HumanSession` resource and reconciler |
| Git-related workflow ideas | Deterministic capability/utility jobs |
| Prometheus scaffolding | Operational telemetry, separate from audit records |

## Open architectural decisions

- **AUTHOR NOTE:** Select the minimum CRD set for September versus types deferred to post-MVP.
- **AUTHOR NOTE:** Decide whether workflow definitions are admitted directly as CRDs or submitted through an API that validates and materializes them.
- **AUTHOR NOTE:** Define the persistence layer for artifacts and audit events after the PVC-based MVP.
- **AUTHOR NOTE:** Define how project policy is authored and evaluated; Kubernetes admission policy may cover infrastructure constraints but not all workflow transitions.
- **AUTHOR NOTE:** Define lifecycle and retention after completion: namespace TTL, artifact retention, audit retention, and validation teardown.
