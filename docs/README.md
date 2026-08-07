# Documentation map

This repository separates durable architectural intent from component designs, interface contracts, operational instructions, and historical decisions. That separation keeps the architecture readable while allowing detailed implementations—such as the service mesh or MCP server—to evolve independently.

## Documentation levels

### 1. Product and scope

Use the repository [README](../README.md) and [September MVP](mvp.md) for the project thesis, intended user, current scope, demonstration, and non-goals.

These documents answer: **what are we building, for whom, and what must the next milestone prove?**

### 2. System architecture

Use [`docs/architecture`](architecture) for stable system boundaries, invariants, trust assumptions, state models, and cross-component responsibilities.

These documents answer: **why is the system divided this way, and what must remain true across implementations?**

Current architecture documents:

- [Overview](architecture/overview.md)
- [Execution model](architecture/execution-model.md)
- [Security model](architecture/security-model.md)
- [Inference architecture](architecture/inference.md)
- [Failure model](architecture/failure-model.md)
- [Audit model](architecture/audit-model.md)

Architecture documents should not become installation guides or line-by-line component specifications.

### 3. Component designs

Detailed designs belong under `docs/components/<component>.md`. A component design explains one deployable service or tightly cohesive subsystem:

- responsibilities and non-responsibilities;
- dependencies and trust boundaries;
- internal architecture;
- APIs and watched resources;
- identity and authorization;
- reconciliation or request flow;
- configuration;
- failure behavior;
- observability and audit events;
- deployment topology; and
- testing strategy.

Planned component documents:

| Document | Subject |
| --- | --- |
| `components/workflow-controller.md` | reconcilers, ownership, status, finalizers, and idempotency |
| `components/agent-runtime.md` | Go wrapper, process supervision, and input/output contract |
| `components/mcp-server.md` | tool registry, authorization, schemas, limits, and auditing |
| `components/service-mesh.md` | workload identity, mTLS, authorization policy, and human-session connectivity |
| `components/human-session.md` | session lifecycle, VS Code access, workspace mounting, and TTL |
| `components/context-service.md` | context request, assembly, policy, versioning, and provenance |
| `components/audit-service.md` | event ingestion, persistence, query, playback, and retention |
| `components/inference-provider-vllm.md` | endpoint lifecycle, sharing, health, and capacity |
| `components/validation-provider-argocd.md` | Argo CD/Kustomize project contract, deployment, health, and cleanup |
| `components/git-utility.md` | credential isolation, branch/commit/merge operations, and idempotency |
| `components/gpu-observer.md` | hardware telemetry, health conditions, and pressure signals |

Create a component document when implementation needs more detail than an architecture boundary but the decision does not apply to the entire system.

### 4. Interface and schema specifications

Machine-facing contracts belong under `docs/interfaces` and should be versioned alongside their implementation:

- CRD field and condition semantics;
- gRPC/API behavior;
- agent `input.json` and `result.json` schemas;
- artifact contracts;
- audit event schemas;
- MCP tool schemas;
- provider interfaces; and
- labels, annotations, and identity formats.

These documents answer: **what exact contract must two components obey?** Whenever possible, generate reference documentation from schemas or source definitions instead of duplicating them manually.

### 5. Operations and runbooks

Deployment and operator procedures belong under `docs/operations`:

- installation and upgrades;
- air-gapped image/model mirroring;
- GPU and MIG configuration;
- service-mesh bootstrap;
- backup, retention, and disaster recovery;
- failure diagnosis;
- credential rotation; and
- demonstration or chaos-test runbooks.

These documents answer: **how is the system installed, configured, operated, and repaired?**

### 6. Architecture decision records

Use [`decisions`](../decisions) for one consequential choice, its alternatives, and consequences. An ADR records **why one option was selected**; the architecture document records the resulting system; a component document describes how that decision is implemented.