# Minimum working demo plan

This document captures the narrowed demonstration plan for SovereignAI. It is intentionally smaller than the full MVP 1 plan.

The demo should prove this thesis:

> AI workflows need a control plane because useful autonomous work requires governed execution, durable state, recovery, human control, and explainable lineage -- not just prompt chaining.

## Demo target

The demonstration should show:

1. A developer creates a project and submits a workflow.
2. SovereignAI creates an isolated execution namespace.
3. Real agents perform meaningful work against the SovereignAI repository.
4. Typed artifacts move between workflow steps.
5. Deterministic platform steps test, build, commit, and merge.
6. Argo CD/Kustomize creates an ephemeral preview environment.
7. A controlled failure occurs during execution.
8. The platform detects the failure, records it, retries from a safe boundary, and completes.
9. CLI playback reconstructs what happened using decision lineage.

The platform, not the agent, is the star of the demo.

## Reduced canonical workflow

The full target workflow is:

```text
initialize -> branch -> architect -> test writer -> developer -> agent review -> tests
-> human gate/intervention -> commit/push -> build/verification
-> Argo CD/Kustomize preview -> product approval -> merge
```

For the minimum working demo, use this narrower sequence:

```text
initialize workspace
-> create branch
-> architect agent
-> developer agent
-> reviewer agent
-> test job
-> commit job
-> build/image job
-> Argo CD/Kustomize preview
-> product approval gate
-> merge job
```

Full code-server human intervention is deferred. A product approval gate is sufficient for the first demo because it proves human control without bringing in the full identity, gateway, and interactive workspace stack.

## Failure mode

Preferred failure:

```text
delete the active vLLM pod during the developer or reviewer step
```

Expected recovery:

1. `InferenceEndpoint` observes the failed or missing pod.
2. Active leases bound to that endpoint are interrupted.
3. The active agent attempt is interrupted.
4. The workflow creates a new attempt.
5. A recovered or recreated endpoint is used.
6. The workflow retries from the previous accepted boundary.
7. Playback shows the causal chain.

Fallback failure if vLLM recovery is too cluster-specific:

```text
delete the active agent pod during a step
```

This is less dramatic, but still proves the control-plane recovery story.

## Implementation phases

### 1. Demo-clean repository

- Remove or untrack generated executables such as `controller.exe`.
- Confirm ignore rules for local binaries and generated build outputs.
- Replace the minimal example workflow with the actual demo workflow.
- Ensure the example project manifest matches the demo repository setup.
- Add a short demo script document.
- Keep `go test ./...` passing.
- Keep image builds reproducible enough for the demo environment.

### 2. Decision lineage first

The demo depends on lineage more than polish. Add audit emission before the workflow becomes complex.

Minimum event vocabulary:

```text
WorkflowSubmitted
NamespaceCreated
WorkflowAdmitted
WorkspaceCreated
StepAttemptCreated
StepAttemptStarted
StepAttemptPodCreated
StepAttemptSucceeded
StepAttemptFailed
StepAttemptInterrupted
StepAttemptRetried
InferenceLeaseRequested
InferenceLeaseAdmitted
InferenceEndpointCreated
InferenceEndpointReady
InferenceLeaseBound
InferenceEndpointLost
InferenceLeaseInterrupted
ArtifactDeclared
ArtifactAccepted
ArtifactRejected
UtilityJobCreated
UtilityJobSucceeded
UtilityJobFailed
ValidationRunCreated
ValidationRunReady
ValidationRunFailed
HumanApprovalRequested
HumanApprovalGranted
HumanApprovalDenied
GitBranchCreated
GitCommitCreated
GitMergeCompleted
RecoveryDecisionSelected
```

For the demo, the existing audit recorder abstraction is enough. A separate audit microservice is not required unless it becomes necessary for deployment.

Required user-facing feature:

```text
sovctl playback <workflow-id>
```

Playback should be ordered by event time and event ID. It should include references to workflow, step, attempt, pod, job, lease, endpoint, artifact digest, policy decision, commit SHA, image digest, validation URL, and telemetry window where available.

### 3. StepAttempt lifecycle correctness

- Treat interrupted inference leases as interrupted attempts.
- Detect lost or failed inference endpoint pods.
- Mark affected leases interrupted.
- Terminate active agent pods when their inference lease is interrupted.
- Make interrupted attempts retryable when the step allows retry.
- Ensure retries create new `StepAttempt` resources.
- Preserve failed/interrupted attempts as historical evidence.
- Emit audit events on all meaningful phase transitions.

Demo invariant:

```text
developer-001 is interrupted
developer-002 is created
developer-001 remains visible in CRDs and playback
```

### 4. Reference agent

Add a small first-party reference agent:

```text
cmd/reference-agent
```

Minimum responsibilities:

- read `input.json`;
- call an OpenAI-compatible vLLM endpoint;
- optionally use MCP context tools;
- write only to the declared staging path;
- write valid `result.json`.

Minimum roles:

```text
architect
developer
reviewer
```

Suggested behavior:

- architect produces an implementation plan artifact;
- developer makes a small code, doc, or test change;
- reviewer produces a review artifact and approves the change.

The reference agent should be intentionally boring. The platform behavior is the demo.

### 5. MCP/context

The current MCP/context server is sufficient as a demo starting point.

Demo requirements:

- expose tree, read, search, metadata, and bundle operations;
- make the server reachable by agent pods;
- record the context bundle or context calls in lineage;
- use a simple attempt identity mechanism.

Acceptable demo simplification:

```text
X-Sovereign-Attempt: developer-001
X-Sovereign-Workflow: controller-change-x9a2p
```

The production design can later replace this with Istio/SPIFFE workload identity.

### 6. Deterministic utility runner

Add a first-party utility runner:

```text
cmd/utility-runner
```

Minimum subcommands:

```text
init-workspace
create-branch
run-tests
commit
build-image
merge
```

Requirements:

- use deterministic branch names;
- write structured result files;
- be idempotent under reconciliation retries;
- avoid duplicate commits and duplicate merges;
- emit or cause audit events for each operation.

For `build-image`, prefer a real local registry if it is smooth. If image registry work threatens the demo timeline, use a deterministic pseudo image digest generated from the commit SHA and build inputs, then clearly label it as a demo simplification.

### 7. Argo CD/Kustomize preview

Use the existing `ValidationProvider` direction.

Required additions:

- pass candidate commit and image digest from utility artifacts into `ValidationRun`;
- create a deterministic workflow-scoped Argo Application;
- report sync/health status;
- expose a review URL;
- clean up on workflow deletion;
- emit validation events into lineage.

No additional validation providers are required for the demo.

### 8. Product approval gate

Implement approval before full intervention.

Required API/CLI:

```text
sovctl approve <workflow-id> --step product-approval
```

Expected behavior:

- a human gate attempt enters `AwaitingApproval`;
- merge cannot proceed until approval is recorded;
- approval transitions the gate to `Succeeded`;
- playback shows who approved and when.

For demo, a static development subject or simple bearer token is acceptable. Full Keycloak/OIDC is deferred.

### 9. Demo script and polish

The demo script should end with playback.

Suggested sequence:

1. Show the project and workflow definition.
2. Submit the workflow.
3. Show the isolated namespace and child resources.
4. Show an agent step running.
5. Show accepted artifacts.
6. Delete the active vLLM pod.
7. Show interrupted attempt and retry attempt.
8. Show Argo CD/Kustomize preview.
9. Approve the product gate.
10. Merge.
11. Run `sovctl playback <workflow-id>`.

The final playback should be the proof moment.

## Explicit demo cuts

The following are not required for the minimum working demo:

- full Keycloak deployment;
- full Istio strict mTLS and AuthorizationPolicies;
- service mesh CSR/SPIFFE flow;
- full code-server human intervention;
- dynamic workflow mutation;
- parallel steps;
- llm-d integration;
- cloud provider support;
- advanced GPU scheduling;
- NVIDIA time-slicing;
- CUDA driver crash demo;
- rich web playback UI;
- comparative workflow analytics;
- long-term retention and integrity model;
- external CI provider abstraction;
- multiple validation providers;
- production-grade admission webhooks;
- sophisticated context engine;
- multi-tenant enterprise RBAC;
- SBOM/vulnerability pipeline unless already easy.

## Simple demo implementations

| Area | Demo implementation |
| --- | --- |
| Identity | static dev subject or simple bearer token |
| Policy | embedded OPA with a few hardcoded MVP rules |
| Audit | existing recorder plus PostgreSQL-backed CLI timeline |
| Human control | approval gate first |
| Agent | one small reference agent with role flags |
| Context | existing MCP tree/read/search/bundle tools |
| Inference | direct vLLM only |
| Validation | Argo CD/Kustomize only |
| Git | deterministic utility runner |
| Build image | real local registry if easy; pseudo digest if necessary |
| Telemetry | link/correlation window, not full analytics |

## Priority order

1. Audit event emission and playback CLI.
2. StepAttempt interruption/retry correctness.
3. Reference agent.
4. Deterministic utility runner.
5. Full demo workflow manifest.
6. ValidationRun consumes commit/image outputs.
7. Approval API/CLI.
8. vLLM failure recovery.
9. Demo script and polish.

## Three-week execution estimate with AI acceleration

This schedule assumes AI accelerates ordinary code writing, test scaffolding, refactoring, and documentation. It does not assume AI removes the normal integration tax around Kubernetes, image builds, Argo CD, vLLM, GPU runtime behavior, or demo-environment drift.

### Week 1: lineage and lifecycle foundation

Estimated effort:

- focused solo work: 18-25 hours;
- compressed but risky: 12-15 hours;
- comfortable: 25-35 hours.

Outcomes:

- audit events emitted for workflow, step, lease, artifact, utility, validation, approval, and recovery transitions;
- `sovctl playback <workflow-id>` exists;
- interrupted inference leases cause interrupted/retryable attempts;
- lost agent pods produce explainable retry behavior;
- existing tests remain green;
- new unit/fake-client tests cover lineage ordering and retry creation.

Demo value at end of week:

The platform can explain a failed or retried attempt even before the full workflow is beautiful.

### Week 2: real execution path

Estimated effort:

- focused solo work: 22-30 hours;
- compressed but risky: 16-20 hours;
- comfortable: 30-40 hours.

Outcomes:

- `cmd/reference-agent` exists with architect/developer/reviewer roles;
- reference agent can call a fake OpenAI-compatible server in tests;
- reference agent can call vLLM in the demo environment;
- MCP/context use is minimally wired;
- `cmd/utility-runner` supports init workspace, branch, tests, commit, build digest, and merge;
- demo workflow manifest uses the reduced canonical sequence;
- artifacts carry enough metadata for downstream steps.

Demo value at end of week:

A real workflow can run mostly end-to-end with meaningful artifacts and deterministic platform actions.

### Week 3: validation, approval, failure, and rehearsal

Estimated effort:

- focused solo work: 25-35 hours;
- compressed but risky: 18-25 hours;
- comfortable: 35-45 hours.

Outcomes:

- ValidationRun consumes candidate commit/image digest;
- Argo CD/Kustomize preview works in the demo environment;
- approval API/CLI exists;
- merge is blocked until approval;
- vLLM pod deletion or agent pod deletion is a repeatable failure showcase;
- playback reconstructs the successful workflow and the failure/retry path;
- demo script is written and rehearsed;
- at least one full reset/re-run path is documented.

Demo value at end of week:

The demo can tell the core story: governed workflow, real agent work, failure, recovery, approval, preview, merge, and decision lineage.

## Three-week feasibility

Three weeks is feasible for a minimum working demo if scope discipline is strict.

Expected total effort:

- credible but intense: 65-90 hours;
- risky compressed version: 45-60 hours;
- comfortable version: 90-120 hours.

The riskiest areas are:

1. vLLM/GPU runtime recovery;
2. Argo CD/Kustomize preview wiring;
3. idempotent Git operations;
4. making playback useful instead of decorative.

If time gets tight, cut in this order:

1. full vLLM failure, replacing it with agent pod deletion;
2. real image push, replacing it with pseudo digest;
3. reviewer agent sophistication;
4. MCP audit depth;
5. product approval identity strength.

Do not cut playback. Playback is the proof surface for the control-plane thesis.
