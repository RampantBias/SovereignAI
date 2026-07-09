# Execution model

## Goals

The execution model makes autonomous work resumable, attributable, and bounded. It distinguishes declared workflow intent from individual execution attempts and separates agentic reasoning from deterministic privileged operations.

The MVP supports predefined, sequential workflows. Parallel branches and dynamically amended plans are post-MVP concerns.

## Workflow definition

A workflow definition should declare:

- project and repository target;
- ordered steps;
- step execution kind;
- typed inputs and outputs;
- required capabilities;
- model and inference policy;
- retry and timeout policy;
- approval and validation gates; and
- workspace and retention requirements.

Illustrative—not final—shape:

```yaml
apiVersion: sovereign-ai.io/v1alpha1
kind: SovereignWorkflow
metadata:
  name: controller-change
spec:
  projectRef: sovereign-ai
  repositoryRef: platform
  steps:
    - name: architect
      kind: Agent
      agentRuntime: default
      modelPolicy: reasoning-small
      inputs:
        - contract: change-request/v1
      outputs:
        - contract: implementation-plan/v1
      capabilities:
        - context.read
    - name: create-branch
      kind: Utility
      utility: git.create-branch
    - name: test-writer
      kind: Agent
      inputs:
        - contract: implementation-plan/v1
      outputs:
        - contract: patch/v1
        - contract: test-plan/v1
    - name: developer
      kind: Agent
      inputs:
        - contract: implementation-plan/v1
        - contract: test-plan/v1
      outputs:
        - contract: patch/v1
    - name: tests
      kind: Utility
      utility: test.execute
    - name: approval
      kind: HumanGate
    - name: commit
      kind: Utility
      utility: git.commit
    - name: preview
      kind: Validation
      provider: argocd
```

The exact ordering for the MVP remains open; see [MVP scope](../mvp.md).

## State machines

### Workflow phases

```text
Pending -> Provisioning -> Running -> AwaitingApproval -> Validating -> Succeeded
    |            |           |              |                |
    +------------+-----------+--------------+----------------+--> Failed
                             |
                             +--> Intervening --> Running
                             |
                             +--> Cancelling --> Cancelled
```

Not every workflow visits every phase. Phase names must have one unambiguous meaning and be encoded as tested constants.

### Step phases

```text
Pending -> Ready -> Running -> Succeeded
                   |   |
                   |   +--> AwaitingHuman -> Succeeded / Failed
                   |
                   +--> Failed -> Retrying -> Ready
```

A retry creates a new `StepAttempt`. It does not reset or erase the prior attempt.

### Attempt phases

```text
Created -> Admitted -> Preparing -> Executing -> Collecting -> Succeeded
                         |             |             |
                         +-------------+-------------+--> Failed
                                       |
                                       +--> Interrupted
```

`Interrupted` identifies deliberate platform or human interruption. It is distinct from an infrastructure failure.

## Agent runtime contract

The Go wrapper supervises an arbitrary agent executable supplied by a custom image. It enforces a platform contract without dictating the agent framework.

The initial file contract is:

- `/workspace/control/input.json`: immutable task and context references;
- `/workspace/control/result.json`: terminal result and artifact declarations;
- `/workspace/control/events.jsonl`: optional structured runtime events;
- `/workspace/artifacts/...`: produced files; and
- a process exit code interpreted alongside `result.json`.

The wrapper is responsible for:

- validating the input contract;
- launching and supervising the configured executable;
- forwarding termination with a bounded grace period;
- recording process metadata and terminal status;
- validating declared outputs and calculating digests;
- refusing undeclared privileged behavior where enforceable; and
- emitting attempt lifecycle events.

The wrapper must not contain agent reasoning or Git credentials.

**AUTHOR NOTE:** Define the executable discovery mechanism: fixed path, image annotation, workflow field, or OCI image contract.

## Artifact contracts

Artifacts should be formally typed even when a step may produce a variable number of them. A contract contains:

- stable contract name and version;
- media type or schema;
- cardinality;
- producer step and attempt;
- content digest;
- storage reference;
- security classification; and
- validation status.

Examples include `implementation-plan/v1`, `patch/v1`, `test-report/v1`, `review/v1`, and `validation-result/v1`.

Variable output is represented as a collection conforming to a declared contract, not as untyped files discovered after execution.

**AUTHOR NOTE:** Choose the MVP schema system. JSON Schema is a practical first choice because the wrapper already exchanges JSON, but OCI artifacts or protobuf may be useful later.

## Deterministic utility steps

Utility steps execute narrow, audited operations without model inference. Git is the first important example.

Agents may inspect repository material through read-only context capabilities and produce proposed patches. They do not receive repository credentials. Trusted utility jobs perform operations such as:

- clone or initialize workspace;
- create a workflow branch;
- apply an accepted patch;
- commit with workflow and artifact provenance;
- push a branch;
- create a merge request; and
- merge only after required policy, approval, CI, and validation.

Every utility step receives a typed request, a least-privilege credential, a restricted network policy, and a terminal output contract.

## Human gates and intervention

A human gate asks an authorized person to approve, reject, or request intervention.

Intervention follows this sequence:

1. stop accepting new autonomous actions;
2. terminate or quiesce the active agent attempt at a defined boundary;
3. record the last accepted workspace revision;
4. authorize and create a separately identified `HumanSession` pod;
5. mount the same workspace and expose approved development tooling;
6. record attributable human changes and explicit capability approvals;
7. close the session and produce a human-authored artifact or workspace revision; and
8. resume through a new agent attempt or advance through approval.

The autonomous agent does not silently become the human's identity. A conversational assistant may run within the human session, but each requested action remains attributable and policy-controlled.

## Context delivery

For the MVP, step input combines:

- contractually required artifacts from previous steps; and
- context returned by a controlled context service.

The context engine is intentionally under-specified. The workflow controller requests context but does not construct it itself. Context access is capability-checked and audited, including the query, source references, classification, and consumer attempt where feasible.

**AUTHOR NOTE:** Define the smallest MVP context implementation. A curated repository snapshot plus explicit file retrieval may demonstrate governance more clearly than introducing an immature RAG system.

## Controlled dynamism after MVP

Dynamic planning can preserve determinism through immutable workflow revisions:

1. an agent proposes a typed `WorkflowAmendment`;
2. policy validates the allowed change surface;
3. a human or deterministic gate accepts it;
4. the platform creates a new immutable workflow revision; and
5. execution continues against that revision.

No agent may silently rewrite its active graph.

## MVP workflow questions

- **AUTHOR NOTE:** Freeze the exact step order and identify which stages are agent, utility, gate, or validation steps.
- **AUTHOR NOTE:** Define retry limits and which failures are safe to retry automatically.
- **AUTHOR NOTE:** Decide whether reviewer is an autonomous agent, a human gate, or both in sequence.
- **AUTHOR NOTE:** Define when patches are applied to the shared workspace and how conflicting patches are rejected.
- **AUTHOR NOTE:** Define the contract for resuming after human changes.

