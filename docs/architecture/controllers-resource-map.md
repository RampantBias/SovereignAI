# Controllers Resource Mapping
```mermaid
    flowchart LR

    subgraph Controllers
        direction TB
        projectController[Project Controller]
        workflowController[Workflow Controller]    
        stepAttemptController[StepAttempt Controller]
        inferenceLeaseController[InferenceLease Controller]
        inferenceEndpointController[InferenceEndpoint Controller]
        agentRunController[AgentRun Controller]
        utilityOperationController[UtilityOperation Controller]
        humanSessionController[HumanSession Controller]
        validationRunController[ValidationRun Controller]
        artifactController[Artifact Controller]
        approvalRequestController[ApprovalRequest Controller]
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
        utilityOperation[Utility Operation]
        inferencePod[Inference Pod]
        collectorJob[Collector Job]
        workspacePvc[Workspace PVC]
    end

    subgraph WorkflowExecution
        direction TB
        workflowController -->|reconciles| sovereignWorkflow
        workflowController -->|creates/manages| stepAttempt

        sovereignWorkflow -->|owns/creates| stepAttempt
        sovereignWorkflow -->|owns/creates| workspacePvc
        sovereignWorkflow -->|references| stepAttempt

        stepAttempt -->|owns/creates| agentPod
        stepAttempt -->|owns/creates| utilityOperation
        stepAttempt -->|owns| collectorJob

        inferenceLeaseController -->|owns| inferenceLease
        inferenceLeaseController -->|references| inferencePod

        inferenceEndpointController -->|owns| inferenceEndpoint
        inferenceEndpointController -->|references| inferencePod
    end

```

## Changes to primary and owned resources enqueue reconciliation through controller-runtime watches.