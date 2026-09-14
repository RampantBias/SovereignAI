# SovereignProject Lifecycle

This diagram focuses on detailing the lifecycle of a SovereignProject resource. The SovereignProjectReconciler is the system which
holds the capabilities and policies that allow the control plane to reconcile this resource.

```mermaid
stateDiagram-v2
    [*] --> Active: Project created
    Active --> Deleting: Deletion requested
    Deleting --> Deleting: Cleanup incomplete
    Deleting --> [*]: Cleanup complete / remove finalized / object deleted

    state Active {
        [*] --> Assessing
        Assessing --> Invalid: Configuration fails validation
        Assessing --> AwaitingProvider: Configuration is valid

        Invalid --> Assessing: Spec or dependency changes

        AwaitingProvider --> AwaitingProvider: Provisioning fails / record reason and retry
        AwaitingProvider --> Ready: Validation project successfully ensured
        AwaitingProvider --> Assessing: Spec or dependency changes

        Ready --> Assessing: Spec or dependency changes
        Ready --> Ready: Periodic reconciliation succeeds
        Ready --> AwaitingProvider: Provider reconciliation fails
    }

    note right of Ready
        ConfigurationValid = True
        ValidationProviderReady = True
        Both reflect current configuration.
    end note
```