# Failure model

## Objective

SovereignAI should make failures explicit, bounded, recoverable where safe, and visible to operators. Recovery means restoring a consistent workflow state without silently duplicating accepted work or losing attribution. It does not mean every agent process resumes at the exact instruction where it stopped.

## Core guarantees

The target control plane aims to provide:

- idempotent reconciliation;
- at-least-once controller processing with duplicate-safe effects;
- immutable attempt history;
- no retry of a privileged side effect without an idempotency strategy;
- explicit terminal and intervention states;
- separation of observed failure from selected recovery policy; and
- durable evidence for every recovery decision.

Exactly-once execution is not claimed. Deterministic utility operations must use idempotency keys and verify external state before retrying.

## Failure taxonomy

| Domain | Examples | Initial response |
| --- | --- | --- |
| Control plane | controller crash, leader loss, API restart | reconstruct from Kubernetes state and continue reconciliation |
| Agent attempt | process exit, timeout, invalid result contract | fail attempt; retry or intervene according to policy |
| Inference | vLLM crash, endpoint unhealthy, request failure | stop affected attempt; reacquire capacity; retry if permitted |
| GPU/node | CUDA driver failure, unhealthy device, node loss | stop admission, mark capacity unavailable, terminate/retry affected work |
| Workspace | PVC mount failure, corruption, full volume | stop workflow and require repair/intervention |
| Utility operation | Git timeout, ambiguous push, CI API failure | inspect external state using idempotency key before retry |
| Validation | deployment failure, health timeout | record failed validation; preserve review evidence |
| Human session | disconnect, TTL expiry | close access; preserve workspace; require explicit resume |
| Policy/audit | policy unavailable, audit sink unavailable | fail closed for privileged progression |

## Reconciliation and reconstruction

After controller loss, Kubernetes custom resources remain the MVP source of desired state and summarized progress. The returning controller lists managed resources, observes subordinate state, and reconciles toward the declared phase.

Every created resource requires stable labels, owner relationships where valid, and deterministic names or recorded references. A reconciler must tolerate:

- resource already exists;
- status update conflict;
- stale informer cache;
- child exists but status was not recorded;
- status recorded but child was deleted; and
- repeated delivery of the same event.

The controller must never infer successful agent work solely from pod success. It validates the result contract and registered artifact digests.

## Attempt and retry semantics

A failure closes the current attempt. Retry creates a new attempt with:

- incremented attempt number;
- explicit reason and policy decision;
- declared input artifact revisions;
- fresh identity and capability grants;
- no inherited transient credentials; and
- linkage to the prior attempt.

Retries are safe only if the step's effects are isolated in the workspace or protected by an idempotent platform operation. Unknown external side effects require human intervention.

## GPU-pressure policy

For the MVP, pressure handling is deliberately simple:

- do not admit new work when safe capacity is unavailable;
- wait or reject according to the workflow's timeout policy;
- on demonstrated endpoint/device failure, terminate the affected agent attempt;
- release or invalidate its inference lease;
- reacquire healthy capacity; and
- retry from the beginning of the step when its policy permits.

Flexible eviction, checkpoint-driven continuation, prioritization, and preemption are later capabilities.

## Checkpoints

An agent-produced checkpoint is an advisory artifact, not proof of safe continuation. A future checkpoint contract must specify model/runtime compatibility, accepted workspace revision, context summary, pending action, and integrity digest.

The MVP may retry from a completed step boundary and should not promise mid-step continuation.

## September failure demonstration

The demonstration should include a reproducible failure with a visible audit trail.

### Scenario A: controller loss

1. Start a workflow and advance into an active agent step.
2. Delete the workflow controller pod.
3. Show that the active workload is not duplicated.
4. Allow Kubernetes to restart the controller.
5. Show reconstruction from CRDs and child resources.
6. Complete the workflow without manual state repair.

This proves idempotent control-plane recovery, not merely pod restart.

### Scenario B: inference failure

1. Start an agent attempt using a local inference endpoint.
2. deliberately terminate or make the endpoint unhealthy;
3. detect the loss and stop the affected attempt;
4. record the failure and selected policy;
5. reacquire or recreate inference capacity;
6. create a new attempt; and
7. complete from the last accepted step boundary.

This is more reproducible than intentionally crashing the CUDA driver. A real driver/Xid failure can be an additional demonstration after the controlled scenario is reliable.

Expected event sequence:

```text
StepAttemptStarted
InferenceEndpointUnhealthy
RecoveryPolicySelected
StepAttemptInterrupted
InferenceLeaseAcquired
StepAttemptRetried
StepAttemptSucceeded
```

## Chaos and recovery testing

The architecture should eventually be validated with repeatable fault injection:

- delete controllers at each reconciliation boundary;
- delete child pods before and after status updates;
- force API conflicts and transient errors;
- kill inference during active requests;
- cordon or remove GPU nodes;
- fill workspace storage;
- interrupt Git and validation operations after ambiguous success; and
- disconnect human sessions.

Tests should assert final state, absence of duplicate side effects, and required audit events—not only process survival.

## Open failure decisions

- **AUTHOR NOTE:** Select the exact September failure scenario and success criteria.
- **AUTHOR NOTE:** Define default retry counts, backoff, timeout, and intervention thresholds.
- **AUTHOR NOTE:** Define which audit or policy failures cause the platform to fail closed.
- **AUTHOR NOTE:** Define workspace backup and corruption recovery expectations.
- **AUTHOR NOTE:** Decide whether failed namespaces are retained for investigation or cleaned after a TTL.

