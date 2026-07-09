## Versioning
Version explicitly only when consumers depend on compatibility:
CRD API versions
agent input/output contracts
artifact schemas
audit-event schemas
MCP tool contracts
external APIs

### For architecture documents, add lightweight metadata:
- status: Draft
- owner: Frank
- last-reviewed: YYYY-MM-DD
- applies-to: MVP

## Public-release gaps
### Release blockers
These are the most important omissions before presenting the repository publicly.

#### Getting Started page
- prerequisites
- supported Kubernetes and GPU stack
- local registry/model requirements
- installation steps
- sample workflow
- expected output
- teardown
- common failure recovery

Ideally, someone unfamiliar with the project can reproduce one minimal workflow.

#### Project Status section
- what currently works
- what is partially implemented
- what is architectural intent only
- known build/runtime limitations
- what the September milestone intends to add

#### End-to-End Example
1. workflow submitted
2. namespace/workspace created 
3. agent attempt starts
4. artifacts produced
5. human gate
6. validation
7. completion and audit timeline

#### Deployment architecture
Add diagrams for:
- control-plane components
- workflow namespace resources
- agent/inference separation
- human-session access
- audit and telemetry flow
- failure recovery

#### Security and threat model
The security document describes desired controls, but public release needs explicit answers to:
- What is trusted?
- What is considered hostile?
- Which guarantees exist today?
- Which are planned?
- What secrets can each component access?
- What happens if an agent image is malicious?
- Is multi-tenancy currently safe?
- Avoid presenting target security as implemented security.

#### API and CRD reference
Document every current field, status condition, default, validation rule, and lifecycle meaning. Include valid examples and identify unstable APIs.

#### Contribution and Governance Basics
At minimum:
- LICENSE
- CONTRIBUTING.md
- SECURITY.md
- code of conduct
- issue/feature-request guidance
- development and test instructions

#### Important credibility material
These do not necessarily block the first publication, but they will substantially improve it.
- Architecture glossary defining workflow, step, attempt, artifact, validation, inference lease, capability, and human session.
- A roadmap separating September MVP, MVP 2, and research directions.
- A known-limitations document.
- A reproducible failure-demo runbook.
- Design rationale explaining why Kubernetes, short-lived agents, deterministic utility steps, and local inference were chosen.
- A normalized ADR collection with dates and clear statuses.
- Testing strategy covering unit, envtest, integration, security, failure injection, and demo acceptance tests.
- Air-gapped operations documentation covering image, chart, dependency, repository, and model mirroring.
- Observability guide explaining current state, audit evidence, telemetry, playback, and evaluation.
- A component inventory showing implemented versus planned ownership.

#### A useful ownership process
As you read, mark each section:
- Accept — matches your thinking.
- Rewrite — correct idea, wrong voice or framing.
- Challenge — assumption you have not accepted.
- Defer — useful, but beyond the current milestone.
- Remove — not part of your architecture.

Then replace every AUTHOR NOTE with a decision, tracked question, or explicit deferral. That process will make the documentation yours far more effectively than polishing sentences.

For public release, I’d prioritize this sequence:
1. Rewrite the README in your voice.
2. Add “implemented versus planned.”
3. Freeze the MVP walkthrough.
4. Repair installation and example workflow documentation.
5. Add threat model and security disclaimers.
6. Add contribution/license/security files.
7. Normalize ADRs.
8. Add detailed component documentation as implementation begins.

The architecture set is currently strongest at explaining the intended system. It is weakest at proving what exists, helping someone run it, and establishing safe expectations. Those are the gaps public readers will notice first.