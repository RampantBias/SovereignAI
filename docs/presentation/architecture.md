
```mermaid
flowchart TD
    A[SovereignWorkflow] --> B[StepAttempt]
    B --> C{Utility \nDeterministic}
    B --> D{Agent \nProbabilistic}
    B --> E{Validation \nTesting}
    C --> F(Artifact)
    D --> F
    E --> F
    
```