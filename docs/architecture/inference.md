# Inference architecture

## Purpose

The inference subsystem provides local model capacity to workflow attempts while maximizing accelerator utilization without violating tenant, classification, or isolation policy.

SovereignAI owns inference intent and governance. It should not permanently own every implementation detail of model serving, request routing, or physical device allocation.

## Three scheduling layers

The architecture distinguishes three decisions that are easy to conflate:

1. **Policy admission:** SovereignAI decides whether a workload may use or share a class of inference capacity.
2. **Device placement:** Kubernetes scheduling and device allocation decide which GPU or MIG device hosts a model-serving pod.
3. **Request routing:** an inference provider or gateway decides which compatible serving replica handles a request.

The current controller combines portions of these concerns through a `GPUNode` ledger and direct pod placement. The target design separates them.

## Inference request

An agent step requests logical capacity rather than a concrete GPU index:

```yaml
model: llama-family/code-small
tenant: application-platform
classification: internal
isolation: SharedWithinTenant
priority: normal
estimatedContextTokens: 16000
estimatedOutputTokens: 4000
hardwareClass: nvidia-gpu
```

The policy layer resolves this into an `InferenceLease`. The lease identifies the approved endpoint and the assumptions used for admission.

## Provider abstraction

The MVP provider may manage direct vLLM pods and Services. A later provider may use llm-d, KServe, or another local inference stack.

Provider responsibilities include:

- acquire or create a compatible endpoint;
- report readiness and health;
- return an authenticated endpoint reference;
- attribute consumption to the requesting attempt;
- enforce or expose capacity signals;
- release the lease; and
- surface provider-specific failures without leaking them into the workflow API.

Provider-neutral workflow status should not contain vLLM pod names as its primary contract.

## Endpoint compatibility and sharing

An existing endpoint is reusable only when all required dimensions match or policy explicitly permits compatibility:

- model revision and quantization;
- tokenizer and adapters;
- tenant/security domain;
- classification;
- isolation mode;
- hardware and runtime configuration;
- capacity and queue policy; and
- cache-retention policy.

Matching only the model name and available VRAM is insufficient.

Suggested sharing modes:

| Mode | Sharing boundary |
| --- | --- |
| `Dedicated` | one workflow or attempt |
| `SharedWithinWorkflow` | attempts in one workflow |
| `SharedWithinProject` | workflows in one project |
| `SharedWithinTenant` | projects in one organizational tenant |
| `SharedWithinClassification` | workloads admitted to the same classification domain |
| `SharedClusterWide` | explicitly permitted cluster-wide use |

## Capacity and VRAM

Physical used VRAM, reserved model memory, and expected dynamic KV-cache demand are different measurements:

- **physical usage:** observed from NVML/DCGM;
- **static reservation:** model weights and runtime overhead;
- **dynamic reservation:** expected KV cache and active-request demand;
- **policy headroom:** safety capacity retained to avoid driver instability or OOM.

The platform must avoid double-counting these values. Physical telemetry detects pressure; admission reservations prevent unsafe placement; provider metrics inform request routing.

For the MVP:

- new work is rejected or waits when safe capacity is unavailable;
- no transparent live migration is promised;
- the demonstration failure policy may terminate an affected attempt and retry it from a defined boundary; and
- thresholds should be configurable rather than compiled into the GPU watcher.

## Kubernetes device allocation

The long-term direction is to express device needs through Kubernetes-native scheduling, using stable device-plugin capabilities and evaluating Dynamic Resource Allocation where supported by the target Kubernetes/NVIDIA stack.

The workflow controller should not set `spec.nodeName` or manually select a GPU as its final architecture. It creates workload intent and observes the scheduler's binding result.

Team-dedicated GPUs or MIG instances can be represented by device classes, node/device attributes, quotas, affinity, or admission policy. Classification constraints must be enforced before scheduling, not inferred after placement.

**AUTHOR NOTE:** Record the exact Kubernetes, NVIDIA GPU Operator/device plugin, MIG, and DRA versions available in the MVP cluster before choosing the integration mechanism.

## GPU health and failure signals

A node-local watcher may continue to publish hardware health and pressure, but it should favor standard telemetry and conditions where available. Signals include:

- device disappearance or unhealthy state;
- driver/Xid errors;
- sustained VRAM pressure;
- serving-process health;
- request queue saturation; and
- repeated model-load failures.

The watcher reports facts. Policy decides whether to wait, reject, terminate, drain, or reschedule.

## llm-d direction

llm-d is a promising provider for post-MVP inference routing and utilization. It offers Kubernetes-native vLLM orchestration, load-aware and prefix-cache-aware routing, flow control, and distributed-serving patterns.

SovereignAI should integrate it rather than duplicate those capabilities when the deployment scale justifies the operational cost. The September MVP should preserve a direct-vLLM path to keep the failure and governance demonstration understandable.

Adoption criteria should include:

- offline installation and mirrored images;
- compatibility with the available GPUs/MIG configuration;
- identity-aware request attribution;
- enforcement of SovereignAI security domains;
- useful health and capacity signals;
- failure behavior under node and driver loss; and
- operational complexity on the small test cluster.

## Open inference decisions

- **AUTHOR NOTE:** Define the MVP model set, expected concurrent workflows, and available GPU topology.
- **AUTHOR NOTE:** Decide whether inference endpoints live in a central inference namespace or tenant-specific namespaces.
- **AUTHOR NOTE:** Define the MVP request-authentication mechanism between agent attempts and shared inference.
- **AUTHOR NOTE:** Define how context length and expected output translate into admission estimates.
- **AUTHOR NOTE:** Decide whether `GPUNode` remains a SovereignAI API, becomes provider-internal, or is replaced by standard device/telemetry APIs.

