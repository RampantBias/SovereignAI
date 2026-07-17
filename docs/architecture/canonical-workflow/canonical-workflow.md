The relationship being controlled.
The exact subject of the decision.
Who authors the desired intent.
Which primitive represents the authority.
Which controller exercises it.
What evidence proves the outcome.

## 1. Initialize repository snapshot
Repository X at revision Y, materialized into workflow workspace Z.

### Declared Inputs:
- Repository URL
- Default revision
- Repository credentials
- Workspace pvc
- Operation selection (StepConfig)
- Output obligation (StepConfig)

### Authority Sequence:
- SovereignProject
  - Authorizes repository, revision, and credential reference
- SovereignWorkflow StepConfig
  - Requests repository.initialize
- StepAttempt
  - Activates this attempt
- UtilityOperation
  - Represents the authority to initialize the repository
- UtilityOperationReconciler
  - Resolves project configuration
  - Evaluates policy
  - Derives idempotency identity
  - Mounts the scoped credential
  - Creates the utility job
- UtilityRunner
  - Initializes and verifies the workspace
- Artifact Controller (collector)
  - Validates and records the repository-revision evidence

### Evidence
Avoid using workspace as evidence and favor immutable records.
- repository-revision/v1
  - repository identity
  - requested revision
  - resolved commit SHA
  - utility operation and attempt identity
  - idempotency key

### Side Effect:
The workspace now contains a detached checkout at the latest commit.


| Property | Decision |
|---|---|
| Relationship | Project repository to workflow workspace |
| Subject | Project repository at its admitted default revision |
| Intent author | Workflow author selects `repository.initialize`; Project author selects repository configuration |
| Authority primitive | `UtilityOperation` |
| Authority controller | `UtilityOperationReconciler` |
| Physical mechanism | Utility Job and registered `repository.initialize` operation |
| Credential authority | Project credential reference, mounted only by the controller |
| Side effect | Detached checkout in workflow workspace |
| Output | `repository-revision/v1` |
| Evidence owner | Trusted collector and Artifact controller |
| Cleanup owner | Workflow namespace/workspace lifecycle |
| Retry rule | Repeated execution must converge to the same repository and revision |

| Claim | Where should it be enforced? | Current state |
|---|---|---|
| Repository comes from the Project | UtilityOperation controller | Enforced |
| Credential comes from the Project | UtilityOperation controller | Enforced |
| Workspace remote still matches the admitted repository | Utility runner | Enforced |
| Requested revision resolves to a commit | Repository initializer | Enforced |
| Result records the resolved commit | Utility result artifact | Enforced |
| Retry converges on the same repository revision | Utility implementation | Enforced |
| Next step consumes this exact accepted artifact | Workflow/artifact handoff | Not enforced |
| Branch begins from the accepted commit | Branch input resolution | Not fully enforced |

### Gaps Exposed:
- next branch step cannot consume a UID-and-digest-bound repository revision artifact
- the branch operation must verify that workspace HEAD still matches the accepted repository revision rather than blindly trusting the workspace state.