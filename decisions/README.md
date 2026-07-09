# Architecture decision index

The files in this directory capture both historical reasoning and planned architecture. They were written while the design was evolving, so their original `Status` fields do not always reflect the current direction.

For current architectural intent, use the documents under [`docs/architecture`](../docs/architecture) and the conversation-derived decisions summarized below. Original records are preserved rather than silently rewritten.

## Status vocabulary

- **Current:** aligned with the present target architecture.
- **Current with amendment:** core decision remains, but details require revision.
- **Historical:** useful context but no longer the implementation direction.
- **Incomplete:** contains an idea but not enough decision detail to govern implementation.
- **Proposed:** requires an explicit author decision.

## Decision map

| Record | Working status | Current interpretation or required action |
| --- | --- | --- |
| `0000-DecisionFormat` | Current with amendment | Add date, decision ID, owners, supersession links, and review notes. |
| `0001-SovereignExecution` | Current | Air-gapped and regulated local execution is the primary target; cloud providers are post-MVP. |
| `0002-KubernetesControlPlane` | Current | Kubernetes is a deliberate substrate, not a temporary adapter. |
| `0003-DumbOrchestratorAsOperator` | Current with amendment | Controllers coordinate intent and do not hold Git/model credentials; clarify provider and capability boundaries. |
| `0004-GoForOrchestratorInfra` | Current | Go remains the primary control-plane language. |
| `0005-HierarchicalStateOwnership` | Historical | Project grouping and workflow isolation survive, but in-memory hierarchy/locking was superseded by CRDs. |
| `0006-IntegratingHumanInTheLoop` | Historical | Superseded by separate human-session pods and identities. |
| `0007-FlexibleModelAgentMapping` | Current with amendment | Shared inference remains a goal, now governed by explicit security-domain policy and provider abstraction. |
| `0008-ProjectThreadSafety` | Historical | Kubernetes optimistic concurrency and reconciliation replace in-memory hierarchical locks. |
| `0010-CommandLineInterface` | Current | CLI is the MVP interface; authentication and future UI remain open. |
| `0011-AgentCommunication` | Current with amendment | Retain versioned input/result contracts; add typed artifacts, wrapper supervision, and provenance. |
| `0017-UsingvLLM` | Current with amendment | Direct vLLM is the MVP provider, not a permanent one-instance-per-GPU architecture. Evaluate llm-d later. |
| `0018-VRAMLedger` | Current with amendment | Preserve admission/pressure distinction; move device placement toward Kubernetes scheduling/DRA and avoid duplicating serving schedulers. |
| `0019-CreateProjectManifests` | Incomplete/current with amendment | Direct Kubernetes creation may remain MVP behavior; project representation and validation-provider ownership need redesign. |
| `001X-AgentCheckpointStrategy` | Proposed | Mid-step checkpoints are post-MVP and require a compatibility/integrity contract. MVP retries at step boundaries. |
| `001X-WorkflowLabelingStrategy` | Current with amendment | Normalize one label domain and complete labels for workflow, step, attempt, actor, and managed resources. |
| `0020-Git-In-The-Loop Credentials` | Current with amendment | Git operations are deterministic utility steps; agents never receive Git credentials. Credentials should be short-lived and operation-scoped. |
| `0021-DecouplingStepArchitecture` | Current | Separate agent, human, utility, inference, and validation planes. Human pods need not be permanently pre-warmed for MVP. |
| `002X-ContextAsAService` | Current with amendment | Context is a controlled service boundary, but the MVP may use curated retrieval rather than full RAG. |
| `0030-MigratingImperativeToDeclarativeCRDs` | Current | CRDs and reconciliation are authoritative; define the smallest coherent resource set. |
| `0035-GPUVRAMEventHandling` | Current with amendment | Node-local watcher reports facts; policy selects recovery. Integrate standard health telemetry where possible. |
| `0036-GPUMultiModelServing` | Incomplete | Replace with an inference-provider and sharing-policy decision. |
| `00XX-Bootstrapper` | Incomplete | Revisit after the MVP installation topology is fixed. |
| `00XX-OrchestratorInfrastructure` | Historical/proposed | Argo CD should not own all control-plane infrastructure by default; retain it as an MVP validation provider. |
| `00XX-OrchestratorReconstruction` | Historical | The motivation remains, but declarative CRDs and subordinate resources now provide reconstruction state. |
| `00XX-ProjectRepresentationToArgoCD` | Incomplete/likely superseded | Projects are workflow groupings; Argo CD is not their canonical representation. |
| `00XX-VRAMAgentEvacuation` | Incomplete | MVP waits/rejects new work and uses terminate/retry for demonstrated failure; flexible eviction is later. |
| `StandardWorkflow` | Current with amendment | Convert into a versioned example workflow matching the final MVP schema and step order. |

## Decisions established by the current architecture discussion

These should become numbered ADRs after review:

1. Kubernetes is the fixed execution substrate.
2. SovereignAI is an autonomous-systems control plane, not an agent reasoning framework.
3. Every workflow receives its own namespace; projects group workflows.
4. MVP workflows are predefined, sequential, and deterministic after admission.
5. Steps declare typed artifact contracts and explicit capabilities.
6. Agent images may be custom, but a trusted Go wrapper enforces the runtime contract.
7. Human intervention uses a separate pod and identity mounting the shared workspace.
8. Git and other privileged repeatable operations run as deterministic utility steps.
9. Argo CD is an MVP `ValidationProvider`, not a permanent core dependency.
10. Inference is provider-backed; direct vLLM is MVP and llm-d is a post-MVP candidate.
11. Inference reuse is policy-controlled across tenant/classification/isolation domains.
12. Kubernetes scheduling/device allocation should replace controller-embedded physical GPU placement.
13. Audit events are authoritative domain evidence and are distinct from logs, metrics, and traces.
14. MVP recovery occurs at step boundaries; sophisticated checkpointing and eviction are deferred.

## Records to write next

- **ADR-0037:** Workflow, step, and attempt execution model.
- **ADR-0038:** Typed artifact contracts and provenance.
- **ADR-0039:** Capability-based agent security.
- **ADR-0040:** Human-session identity and workspace access.
- **ADR-0041:** Deterministic utility operations and Git credentials.
- **ADR-0042:** Validation-provider abstraction and Argo CD implementation.
- **ADR-0043:** Inference-provider abstraction and sharing policy.
- **ADR-0044:** Audit event model and durable sink.
- **ADR-0045:** Failure taxonomy, retry boundaries, and MVP recovery guarantee.

**AUTHOR NOTE:** Assign final numbers only after deciding whether to normalize the existing placeholder-numbered records.

