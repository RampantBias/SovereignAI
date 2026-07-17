# 3. Test Writer Agent

1. This step intends to produce a proposed test patch for an accepted change-request and implementation plan against the exact repository branch and commit recorded by the accepted branch reference.
2. It performs bounded context retrieval and inference, then writes proposed test patch into staging. It must not execute the plan and cannot modify the shared repository, apply the patch, or execute the tests.
3. AgentRun represents the bounded delegation. The accepted artifacts constrain what modifications are made, the capability constrains tool access, and an InferenceLease authorizes model access.
4. AgentRunReconciler validates the delegation, acquires inference, constructs the runtime contract, and creates the supervised agent pod.
5. Success is proved by an accepted unit-test artifact with complete input and runtime provenance. The test-executor consumes the artifact through an immutable handoff.


## Artifact
proposed-tests/v1
- base commit
- patch or deterministic file changes
- affected paths
- test cases and stated intent
- expected test command
- input artifact reference

| Property | Decision |
|---|---|
| Relationship | Accepted requirements and repository state → bounded test author → proposed test changes |
| Exact subject | Tests proposed for change request T and plan P against commit C |
| Intent author | Workflow author |
| Required inputs | Change request, implementation plan, branch reference |
| Primary authority primitive | `AgentRun` |
| Status owner | `AgentRunReconciler` |
| Context authority | Capability grant enforced by authenticated MCP access |
| Inference authority | `InferenceLease` |
| Runtime mechanism | Supervised test-writer agent pod |
| Allowed effects | Context reads, inference calls, staging writes, audit events |
| Forbidden effects | Shared workspace mutation, test execution, Git operations, credentials |
| Durable output | `proposed-tests/v1` or `test-patch/v1` |
| Evidence acceptance | Trusted collector and Artifact controller |
| Immediate consumer | None required |
| Eventual consumer | Test executor |
| Composition inputs | Base revision + proposed tests + developer patch |
| Test execution authority | `UtilityOperation` using the Project test template |
| Retry rule | New attempt, bound to the same accepted input identities |
| Cleanup owner | AgentRun pod/lease lifecycle and workflow retention |


## Gaps Exposed
1. Agent artifact input is not yet resolved and delivered
2. Proposed test patches do not yet have a finalized typed contract
3. Test execution does not yet assemble accepted test and developer artifacts
4. No deterministic patch-application mechanism
5. Patch conflicts and base mismatches lack explicit failure semantics
6. Agent workspace is still writeable
7. Capabilities and MCP identity remain descriptive rather than enforced
8. Source provenance must use the repository commit, not a workflow definition revision