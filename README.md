# SovereignAI

SovereignAI is an experimental Kubernetes control plane for autonomous systems. It explores how AI-driven software workflows can be executed with the reliability, governance, isolation, recovery, and auditability expected of distributed services.

The project operates below agent frameworks such as LangGraph and LangChain. It manages the infrastructure around agent execution: workflow lifecycle, isolated workspaces, constrained capabilities, local inference, typed handoffs, deterministic utility operations, human intervention, validation, and failure recovery.

SovereignAI is designed first for sovereign, air-gapped, and regulated environments. The initial implementation runs local models on Kubernetes-managed GPU infrastructure.

**Project status:** pre-alpha architectural prototype. The repository contains partially implemented controllers, APIs, CLI commands, GPU accounting, and agent-runtime scaffolding. Currently, the demo MVP can be executed, and will complete, but there's still a chance for the agentic operations to repeatedly fail and exhaust retries. The primary failures in the agentic operations are mostly with the harness methodology and MCP issues. 

## Why this exists

Autonomous agents are often treated as application-library concerns. That leaves difficult operational questions unanswered:

- What precisely was an agent allowed to do?
- Which person, service, or model changed an artifact and how?
- Can execution recover without duplicating completed work?
- Can a human safely enter an active workflow and take control?
- How are local accelerators shared without crossing security boundaries?
- How is a proposed change validated before it is accepted?

SovereignAI treats those questions as control-plane responsibilities.

The project's initial goal was to investigate broader hypotheses and questions: 
- Do multiple small, short-lived agents outperform a single large, long-lived agent? Lower tokens, less hallucination, etc. 
- Can we optimize a workflow for a cheaper & smaller model, by enforcing context boundaries and responsibility?
- What information must actually survive a workflow to make it explainable, auditable, and recoverable?

The September MVP is only aimed to experiment with the last hypothesis, while reneging to prove any of the other hypotheses. The infrastructure to study the others is implemented, but not ready to conduct a conclusive study.

## What Works
- Ability to create projects
- Ability to create workflows with change requests from YAML definitions for *mostly limited to Golang projects at this time*
- Workflow can transition between states for the defined contracts
- Failure recovery at the step or workflow level (workflow level has limitations, see retry.md)
- GPU Scheduling of inference workloads
- Rudimentary observability, most of the focus being on lineage (remainder either not integrated here or not yet carried over)

Workflow completion is fairly consistent, but can still encounter occasional failures under the advertised model.

## Intended MVP

The first public demonstration targets an application developer and uses a simple REST calculator app. The aim of the workflow is to add the divide operator support.

A predefined, sequential workflow will:

1. prepare an isolated workspace and feature branch
2. run architect, test-writing, & development agents
3. pass explicit artifacts between stages
4. use constrained local-model inference and a small MCP capability surface as a sidecar
5. perform Git operations through deterministic platform jobs rather than agent credentials
6. pause for human approval or intervention during ephemeral validation
7. expose an ephemeral validation environment through an Argo CD-based provider
8. recover cleanly from a demonstrated control-plane or inference failure
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
| `internal/validation` | Validation Run & Argo CD integration | ArgoCD is in a more complete integration state, allowing a dev deployment of the workload. |
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

Over the course of building this, I've relaxed abstractions and leaked domain specific code across the control plane implementation. At this time, the workflow operation
would only work on the Calculator application and applications that are architecturally similar.

The custom harness is rudimentary and experimental, showing weeks of continual 'bandaids' on the design. It can fail often.

The architecture documents describe the intended direction. Existing code should not be assumed to implement every documented guarantee.

Much of the documentation is incomplete and disjointed. It's something that needs to catch up with where the architecture & operation actually is.

## Future Effort

Although there was no intention for this project to reach beyond an experiment, much of the future work will be to refactor domain elements out of the codebase to bring better domain separation
and clarity in the operations, while allowing for more 'plug and play' for my future research experiments.

## Development Approach and AI Assistance

I conceived, architected, and built the initial implementation of this software by hand. The original control plane binaries, controller code, inference, & all decision documents were written by me and were refined by me with AI review. I built up to basic handoffs and state transitions of generic task/result/artifact contracts between steps. 

As this project is only worked in my free time and I needed to both represent the thesis I synthesized and reach an MVP stage by the first week of September I needed to re-accelerate. I begain using AI much more heavily to handle refactoring, implementation, testing, deployment, and documentation while working towards a September MVP.

I have still remained responsible for the goals, architectural decisions, and accepted behaviors.
