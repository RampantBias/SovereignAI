# September MVP

## Demonstration objective

Demonstrate that SovereignAI can execute a useful autonomous software-development workflow on its own code with credible reliability, security, human control, and auditability.

The MVP is a vertical slice and public architectural demonstration. It is not a production release and does not attempt to prove that small, short-lived agents outperform larger long-lived agents.

## Primary user and environment

- User: application developer/reviewer.
- Workload: a bounded change to the SovereignAI controller or API server.
- Environment: self-managed, air-gapped-capable Kubernetes cluster.
- Hardware: multiple NVIDIA GPUs on at least one node; current development cluster may include multiple nodes.
- Inference: local vLLM provider with policy-controlled endpoint reuse.
- Validation: Argo CD-backed Validation Provider that uses Kustomize to provision an ephemeral deployment for this specific application workflow.

**AUTHOR NOTE:** Record the exact cluster topology, Kubernetes version, GPU models, driver/GPU Operator versions, storage class, service mesh, and local registries used for the demonstration.

## Proposed workflow

The intended stages are:

| Order | Stage | Kind | Primary output |
| --- | --- | --- | --- |
| 1 | Initialize workspace | Utility | repository snapshot and workspace revision |
| 2 | Create feature branch | Utility | branch reference |
| 3 | Architect | Agent | implementation plan and affected-area report |
| 4 | Test writer | Agent | test plan and proposed tests |
| 5 | Developer | Agent | implementation patch |
| 6 | Reviewer | Agent | findings and recommendation |
| 7 | Test executor | Utility | structured test report |
| 8 | Human approval/intervention | HumanGate | approval or human-authored revision |
| 9 | Commit | Utility | attributable Git commit |
| 10 | CI verification | Validation/Utility | CI result |
| 11 | Ephemeral deployment | Validation | access URL and health result |
| 12 | Product review | HumanGate | accept/reject decision |
| 13 | Merge | Utility | merge commit |

This ordering is provisional. In particular, tests may need to execute before and after developer changes, and committing before ephemeral deployment may be required by the Argo CD provider.

**AUTHOR NOTE:** Freeze the demo workflow and decide whether merge is included or the demo stops at an approved merge request.

## Required capabilities

### Workflow lifecycle

- Submit one validated, predefined sequential workflow.
- Create one isolated namespace and PVC-backed workspace per workflow.
- Advance steps idempotently using Kubernetes reconciliation.
- Represent retries as distinct attempts.
- Clean up or retain resources according to an explicit TTL.

### Agent execution

- Supervise an arbitrary agent executable through the Go wrapper.
- Provide a versioned `input.json` contract.
- Validate `result.json` and declared artifacts.
- Use at least two short-lived agent stages.
- Terminate an agent safely for intervention or failure recovery.

### Artifacts and context

- Define schemas for the implementation plan, patch, review, and test report.
- Pass accepted artifacts explicitly between steps.
- Provide controlled repository context through a minimal context interface.
- Record artifact digests and provenance.

### Security and governance

- Run agent pods under a restricted security profile.
- Deny network traffic by default and allow only declared endpoints.
- Expose a small authenticated MCP tool surface.
- Keep Git credentials out of agent pods.
- Attribute agent, utility, controller, and human actions separately.

### Human control

- Pause at an approval gate.
- Stop the autonomous attempt before intervention.
- Create a separately identified, time-limited human-session pod mounting the workspace.
- Provide practical access, ideally through VS Code-compatible tooling.
- Resume using an explicit accepted workspace revision.

### Inference

- Run local vLLM inference.
- Reuse a compatible warm endpoint when policy permits.
- reject or wait when safe capacity is unavailable;
- detect endpoint loss; and
- expose enough usage/health telemetry to explain placement and recovery.

### Deterministic Git and validation

- Create a branch and commit through platform-owned utility jobs.
- Associate Git revisions with workflow and artifact provenance.
- Invoke CI or an equivalent deterministic test operation.
- Deploy an ephemeral Argo CD-backed test environment.
- present a one-click URL or similarly direct review entry point;
- tear down the validation environment safely.

### Audit and recovery

- Persist structured audit events outside ordinary logs.
- Render a human-readable workflow timeline.
- Inject one repeatable failure during the demo.
- Recover without duplicating accepted work.
- Show the observed failure, selected policy, new attempt, and final outcome.

## Failure demonstration

Preferred primary scenario: terminate the active inference endpoint during an agent attempt. SovereignAI detects the loss, interrupts the attempt, invalidates the lease, reacquires healthy capacity, creates a new attempt, and resumes from the previous accepted step boundary.

Secondary scenario: delete the workflow controller during execution and show idempotent reconstruction without duplicate children or repeated utility effects.

A real CUDA driver failure is valuable but should not be the only rehearsed path because it introduces hardware recovery time and demo variability.

**AUTHOR NOTE:** Select the primary scenario and define exact visual success criteria.

## Explicit non-goals

- dynamic agent-created workflow graphs;
- parallel steps;
- general validation for every application type;
- cloud model providers;
- generalized multi-cluster management;
- production-grade context-engine research;
- live migration or transparent mid-token recovery;
- sophisticated GPU preemption or eviction;
- proving comparative agent cost/performance;
- arbitrary unreviewed custom-agent images; and
- complete regulated-industry certification.

## Acceptance criteria

The demonstration is successful when:

1. a developer submits the predefined workflow through the CLI;
2. every workload runs in the intended namespace and identity boundary;
3. agents receive only declared capabilities and never receive Git credentials;
4. typed artifacts advance through the workflow with visible provenance;
5. a human can approve or enter a dedicated session and make attributable changes;
6. deterministic jobs create the branch/commit and validation environment;
7. the injected failure produces a clean retry or recovery without duplicate accepted effects;
8. the final result is testable through the ephemeral environment; and
9. an audit timeline explains the entire execution.

## Delivery sequence

1. Freeze terminology, CRD boundaries, and state machines.
2. Make the workflow vertical slice compile and test through `envtest`.
3. Implement the wrapper and artifact contracts.
4. Implement deterministic workspace/Git jobs.
5. Add direct-vLLM provider and basic inference leases.
6. Add human gate/session.
7. Add Argo CD validation provider.
8. Add durable audit events and timeline view.
9. Add fault injection and recovery tests.
10. Rehearse installation and the complete demo from a clean cluster.

## Risks

| Risk | MVP response |
| --- | --- |
| Scope exceeds September timeline | preserve one workflow, one provider, one validation path |
| Context engine consumes research time | use explicit artifacts and minimal controlled repository retrieval |
| Service mesh/access design expands | choose the smallest secure human-session transport first |
| GPU failure is nondeterministic | use controlled endpoint failure as primary demo |
| Audit implementation becomes a platform | implement one append-only sink and stable envelope |
| Argo CD tightly couples the controller | put it behind `ValidationProvider` immediately |
| CRD status becomes unbounded | keep summaries in CRDs and payloads in artifact/audit storage |

## Decisions needed from the author

- Exact demonstration date and development milestones.
- Exact workflow stage ordering.
- Selected code change used as the demo task.
- Model(s) and agent executable/runtime.
- MVP identity provider and human-session access method.
- Artifact schema technology.
- Audit sink.
- Primary failure scenario.
- Whether CI, product review, and merge all occur live during the presentation.

