# SovereignAI

SovereignAI is an experimental Kubernetes control plane for autonomous systems. It explores how AI-driven software workflows can be executed with the reliability, governance, isolation, recovery, and auditability expected of distributed services.

The project operates below agent frameworks such as LangGraph and LangChain. It does not prescribe how an agent reasons. Instead, it manages the infrastructure around agent execution: workflow lifecycle, isolated workspaces, constrained capabilities, local inference, typed handoffs, deterministic utility operations, human intervention, validation, and failure recovery.

SovereignAI is designed first for sovereign, air-gapped, and regulated environments. The initial implementation runs local models on Kubernetes-managed GPU infrastructure. Cloud inference may become an optional provider later, but it is not an MVP goal.

> **Project status:** pre-alpha architectural prototype. The repository contains partially implemented controllers, APIs, CLI commands, GPU accounting, and agent-runtime scaffolding. It is NOT ready for production use.

## Why this exists

Autonomous agents are often treated as application-library concerns. That leaves difficult operational questions unanswered:

- What precisely was an agent allowed to do?
- Which person, service, or model changed an artifact and how?
- Can execution recover without duplicating completed work?
- Can a human safely enter an active workflow and take control?
- How are local accelerators shared without crossing security boundaries?
- How is a proposed change validated before it is accepted?

SovereignAI treats those questions as control-plane responsibilities.

The project also provides a place to investigate a broader hypothesis: multiple small, short-lived agents may outperform a single large, long-lived agent at lower cost. The September MVP is not intended to prove that hypothesis; it establishes the infrastructure needed to study it later.

## What Works
- Ability to create projects
- Ability to create workflows from YAML definitions
- Workflow state transitions and failure recovery (reconciliation)
- GPU Scheduling of inference workloads
- Rudimentary observability


## Intended MVP

The first public demonstration targets an application developer and uses SovereignAI's own controller or API code as the workload. A predefined, sequential workflow will:

1. prepare an isolated workspace and feature branch;
2. run architect, test-writing, development, review, and test-execution stages;
3. pass explicit artifacts between stages;
4. use constrained local-model inference and a small MCP capability surface;
5. perform Git operations through deterministic platform jobs rather than agent credentials;
6. pause for human approval or intervention;
7. expose an ephemeral validation environment through an Argo CD-based provider;
8. recover cleanly from a demonstrated control-plane or inference failure; and
9. retain attributable evidence of platform, agent, and human actions.

See [MVP scope](docs/mvp.md) for commitments and non-goals.

## Architectural principles

- **Kubernetes is the substrate.** Custom resources express intent and reconciliation restores desired state.
- **Agents receive capabilities, not ambient authority.** Network, identity, tools, credentials, and filesystem access are denied unless explicitly granted.
- **Autonomous and human identities remain distinct.** Human intervention creates a separately identified session rather than impersonating the agent.
- **Agentic and deterministic work are different primitives.** Credentialed operations such as Git commits and merges belong to trusted utility jobs.
- **Artifacts cross step boundaries explicitly.** Inputs and outputs have declared contracts and provenance.
- **Inference is infrastructure.** Model serving is replaceable and governed independently of workflow execution.
- **Failures are workflow events.** Recovery behavior is explicit, observable, idempotent, and policy-driven.
- **Audit evidence is not ordinary logging.** Durable domain events provide attribution; logs and traces support diagnosis.

## Repository map

| Path | Purpose | Current state |
| --- | --- | --- |
| `cmd/controller` | Kubernetes workflow controller | Partial |
| `cmd/api` | gRPC control-plane API | Partial |
| `cmd/cli` | Interactive operator CLI | Partial |
| `cmd/gpu-watcher` | Node-local GPU/VRAM sensor | Partial |
| `cmd/agent-wrapper` | Contract-enforcing agent supervisor | Stub |
| `internal/api/v1alpha1` | Workflow and GPU custom-resource types | Evolving |
| `internal/orchestration` | Reconciliation and resource builders | Evolving |
| `internal/orchestration/inference` | GPU placement and inference lifecycle | Prototype |
| `internal/argo` | Argo CD integration | To become a validation provider |
| `decisions` | Historical and proposed architecture decisions | Requires normalization |
| `docs/architecture` | Current target architecture | Authoritative direction |

## Documentation

- [Documentation map](docs/README.md)
- [Deployment packaging](deploy/README.md)
- [Architecture overview](docs/architecture/overview.md)
- [Execution model](docs/architecture/execution-model.md)
- [Security model](docs/architecture/security-model.md)
- [Inference architecture](docs/architecture/inference.md)
- [Failure model](docs/architecture/failure-model.md)
- [Audit model](docs/architecture/audit-model.md)
- [September MVP](docs/mvp.md)
- [Decision record index](decisions/README.md)

## Current limitations

The current code and the target architecture are not yet aligned. Known gaps include an unfinished agent wrapper, minimal tests, incomplete installation assets, inconsistent example manifests, hard-coded configuration, and controller logic that still combines responsibilities intended for separate providers or resources.

The architecture documents describe the intended direction. Existing code should not be assumed to implement every documented guarantee.

## Author notes

The following project-level details still need an owner decision:

- **AUTHOR NOTE:** Choose the public project name and one-sentence positioning statement used in articles and the September presentation.
- **AUTHOR NOTE:** Add the intended open-source license and contribution policy.
- **AUTHOR NOTE:** Define the supported Kubernetes and NVIDIA software versions for the MVP test environment.
- **AUTHOR NOTE:** Add reproducible build, installation, and demonstration instructions once the architecture realignment is complete.
