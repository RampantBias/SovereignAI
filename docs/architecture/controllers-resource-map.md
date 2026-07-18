# Controllers Resource Mapping
```mermaid
    flowchart LR

    subgraph Controllers
        direction TB
        workflowController[Workflow Controller]    
        stepAttemptController[StepAttempt Controller]
        inferenceLeaseController[InferenceLease Controller]
        inferenceEndpointController[InferenceEndpoint Controller]
        humanSessionController[HumanSession Controller]
        validationRunController[ValidationRun Controller]
        artifactController[Artifact Controller]
    end

    subgraph Sovereign AI CRDs
        direction TB
        sovereignProject[SovereignProject]
        stepAttempt[StepAttempt]
        sovereignWorkflow[SovereignWorkflow]
        agentRun[AgentRun]
        inferenceLease[InferenceLease]
        inferenceEndpoint[InferenceEndpoint]
        humanSession[HumanSession]
        artifact[Artifact]
        policyProfile[PolicyProfile]
        utilityOperation[UtilityOperation]
        approvalRequest[ApprovalRequest]
        approvalDecision[ApprovalDecision]
    end

    subgraph Workloads and Services
        direction TB
        agentPod[Agent Pod]
        inferencePod[Inference Pod]
        collectorJob[Collector Job]
        workspacePvc[Workspace PVC]
    end

    subgraph WorkflowExecution
        direction TB
        workflowController -->|reconciles| sovereignWorkflow
        workflowController -->|creates/manages| stepAttempt

        sovereignWorkflow -->|owns| stepAttempt
        sovereignWorkflow -->|owns| workspacePvc
        sovereignWorkflow -->|references| stepAttempt

        stepAttempt -->|owns| agentPod
        stepAttempt -->|owns| collectorJob

        inferenceLeaseController -->|owns| inferenceLease
        inferenceLeaseController -->|references| inferencePod

        inferenceEndpointController -->|owns| inferenceEndpoint
    end

```

## Changes to primary and owned resources enqueue reconciliation through controller-runtime watches.