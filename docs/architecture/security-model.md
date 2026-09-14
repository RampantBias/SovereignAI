# Security model

## Security objective

SovereignAI assumes agents are untrusted workloads. An agent may be incorrect, compromised, prompt-injected, or actively malicious. The platform must constrain the effect of an agent action to explicitly granted capabilities and preserve attribution for security-relevant events.

The initial deployment target includes sovereign, air-gapped, and regulated environments. Cloud connectivity is not assumed.

This is a target model, not a claim that the current prototype provides these guarantees.

## Trust boundaries

| Component | Trust posture |
| --- | --- |
| Kubernetes control plane | Trusted substrate |
| SovereignAI API and controllers | Privileged control plane; minimized authority |
| Policy and audit services | Trusted platform services |
| Agent image and model output | Untrusted |
| Agent wrapper | Trusted enforcement/supervision component |
| Utility jobs | Trusted only for their narrow declared operation |
| Human-session tooling | Higher-risk interactive workload |
| Inference service | Shared infrastructure inside a declared security domain |
| Validation provider | Trusted integration with provider-scoped authority |
| Repository and artifact contents | Potentially hostile input |

## Identity model

Every actor has a distinct identity:

- authenticated human user;
- control-plane service;
- workflow;
- step attempt;
- human session;
- utility job;
- validation run; and
- inference endpoint.

Workload identities should be issued through Kubernetes service accounts and, when the service mesh is introduced, represented with SPIFFE-compatible identities. Identities are short-lived and bound to the narrowest useful resource.

A human session must never impersonate the active agent. The authenticated user identity is linked to a dedicated human-session workload identity so audit records can answer both who requested the session and which workload performed an action.

**AUTHOR NOTE:** Select the MVP user identity provider and CLI authentication flow. Do not make the service mesh responsible for authenticating the original human.

## Capability model

An agent starts with no ambient capabilities beyond its declared runtime needs. A workflow admission decision translates named capabilities into concrete enforcement:

| Capability | Possible enforcement |
| --- | --- |
| `context.read` | authenticated call to scoped Context API |
| `workspace.write` | writable PVC mount created only for the current workflow Lease holder and writer epoch |
| `inference.invoke` | identity-aware access to approved endpoint |
| `mcp.tool:<name>` | allowlisted MCP method and request schema |
| `artifact.publish` | wrapper-mediated output registration |
| `git.read` | platform-mediated, read-only repository view |
| `git.commit` | utility job only; unavailable to agent |
| `validation.request` | controller/provider operation |

Capabilities are deny-by-default, time-bound, attempt-bound, and auditable. A capability declaration is not sufficient by itself; project and cluster policy must admit it.

The MVP MCP server should expose a very small allowlist. Each tool must define input validation, output limits, authorization checks, timeouts, and audit behavior.

## Pod hardening

Agent pods should target the Kubernetes Restricted Pod Security Standard:

- non-root user;
- read-only root filesystem;
- no privilege escalation;
- all Linux capabilities dropped;
- seccomp `RuntimeDefault` or stricter;
- no host namespace or host-path access;
- explicit CPU, memory, and ephemeral-storage limits;
- distroless or similarly minimal image where compatible;
- signed and pinned images rather than mutable tags; and
- dedicated service account with automount disabled unless required.

The workspace is the primary writable mount. Control inputs should be mounted or protected read-only after initialization.

Human-session pods require more tooling and therefore a wider attack surface. They remain non-privileged, separately labeled, time-limited, restricted to the workflow namespace, and subject to stronger monitoring. Relaxations must be explicit rather than inheriting the agent profile.

## Network policy

Each workflow namespace begins deny-by-default for ingress and egress. Explicit policy permits only required paths such as:

- DNS through the cluster-approved resolver;
- inference endpoint;
- Context API;
- approved MCP endpoint;
- audit/event ingestion where emitted directly;
- validation-specific targets; and
- repository endpoint for deterministic Git utility jobs.

Air-gapped installations must mirror container images, model weights, charts, and source dependencies internally. Runtime model downloads from public registries are not part of the target operating model.

## Shared inference security

An inference endpoint is reusable only when its security domain is compatible with the requesting workflow. Compatibility should consider:

- tenant or organizational team;
- project;
- data classification;
- model and adapter identity;
- isolation mode;
- hardware pool;
- retention/cache policy; and
- regulatory policy.

Initial isolation modes should include `Dedicated`, `SharedWithinProject`, `SharedWithinTenant`, and `SharedWithinClassification`. Cluster-wide sharing should require an explicit policy decision.

The inference gateway/provider must authenticate callers, attribute usage, enforce concurrency and token limits, prevent unauthorized endpoint discovery, and avoid exposing prompts or outputs through metrics and logs.

## Human-session access

Target access flow:

1. the CLI authenticates the human to the SovereignAI API;
2. the API authorizes access to a specific workflow and requested session profile;
3. a `HumanSession` intent is created with requester identity, TTL, and capabilities;
4. the controller creates a dedicated service account and pod;
5. the service mesh issues the pod its own workload identity;
6. the API provides a short-lived, auditable connection mechanism; and
7. session expiry revokes access and tears down credentials and compute.

The human should not submit a CSR asking to assume the agent's SPIFFE ID. That would weaken attribution and couple interaction to the agent lifecycle.

**AUTHOR NOTE:** Select the MVP transport—authenticated API proxy, Kubernetes exec, VS Code tunnel, or another mechanism—and document its threat model.

## Secrets and Git credentials

Agents do not receive Git credentials. A trusted utility job obtains a short-lived credential scoped to one repository and operation. Network policy permits only the repository endpoint. The utility records repository identity, base revision, resulting revision, workflow, approving actor, and artifact digests.

The controller should orchestrate secret references without reading or logging secret values where possible. A future secrets-provider interface may support Vault or an organizational secrets system.

## Admission and supply chain

Recommended target controls include:

- allowed image registries and signature verification;
- immutable image digests;
- workflow schema validation;
- capability admission policy;
- namespace quotas and limit ranges;
- model allowlists and classification rules;
- SBOM and vulnerability policy for runtime images; and
- prevention of mutable `latest` tags.
