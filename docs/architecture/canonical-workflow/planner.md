# 3. Planner Agent

1. Produce an implementation plan for the accepted change request, based on the accepted repository branch and commit, using only admitted repository context.
2. It performs bounded context and inference operations and produces a proposed implementation plan. It must not modify the accepted repository state or exercise any action described by the plan.
3. An immutable AgentRun represents the authority to execute the planner for this task. Accepted task and branch artifacts constrain its knowledge boundary, a capability grant constrains its tools, and an inference lease authorizes model access.
4. The control plane represents delegation with an immutable AgentRun. The AgentRunReconciler validates its StepAttempt authority, acquires any required InferenceLease, constructs the runtime contract, and materializes the supervised agent pod.
5. Success is proven by a contract-valid implementation plan (implementation-plan/v1) bound to the accepted task, branch revision, context, and producing AgentRun. The semantic correctness remains subject to a downstream review.

| Property | Decision |
|---|---|
| Relationship | Accepted task and repository state → bounded autonomous planner → proposed implementation plan |
| Exact subject | Planner AgentRun X operating on task T and repository commit C |
| Intent author | Workflow author |
| Task input | Accepted `change-request/v1` artifact |
| Repository input | Accepted `branch-reference/v1` artifact |
| Context input | Bounded repository reads or an immutable context bundle |
| Primary authority primitive | `AgentRun` |
| Status owner | `AgentRunReconciler` |
| Context authority | Capability grant enforced by authenticated MCP access |
| Inference authority | `InferenceLease` |
| Runtime mechanism | Agent pod supervised by the agent wrapper |
| Allowed effects | Context reads, inference requests, audit emission, staging writes |
| Forbidden effects | Git mutation, credentials, arbitrary Kubernetes access, undeclared network access |
| Durable output | `implementation-plan/v1` |
| Evidence acceptance | Trusted collector and Artifact controller |
| Next consumer | Test-writer AgentRun |
| Retry rule | A retry creates a new attempt and may only reuse the same accepted inputs |
| Cleanup owner | AgentRun owns pod and lease lifecycle; workflow owns workspace retention |


## Gap Exposed
1. The task is not yet a strong input, currently represented by "Responsibility" text box. It needs an input artifact.
2. AgentRun.Spec.Inputs are not yet integrated into the AgentRun controller.
3. Artifact handoff is not enforced (Artifact UIDs and digests).
4. The workspace should be set to read-only.
5. Capabilities are still a simple string implementation, MCP service does not receive an authenticated attempt header.
6. Context retrieval needs more complete provenance to at least partially fulfill desire for decision lineage:
   1. requesting AgentRun
   2. Accepted repository revision
   3. Query or requested path
   4. Returned source references
   5. Context-bundle digest  
7. AgentRun lacks policy admission
   1. image
   2. executable
   3. capabilities
   4. tools
   5. context scope