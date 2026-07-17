## 2. Create feature branch

1. Create or recover local branch X from the exact accepted commit recorded by repository-revision artifact Y.
   - Branch name, accepted base commit, artifact that supplied the commit, retries may encounter an existing branch.

2. It creates or validates a named local branch at the accepted commit and attahes workspace HEAD to that branch.
   - Local git references, meaning of HEAD, active branch in the workspace.

3. Only an admitted UtilityOperation may authorize branch creation. The accepted repository-revision artifact constrains the authorized base commit.
   - Artifact is evidence, not permission.
   - Authority is from: the workflow declaring the branch operation, the active StepAttempt that authors it, the UtilityOperation authorizing this exact utility effect, and the policy which admits the operation and branch parameters.

4. The control plane represents the authority with an immutable UtilityOperation. The UtilityOperationReconciler resolves and admits it, then the registered GitCreateBranch operation performs the local git change.
    - UtilityOperation is the authority-bearing primitive. UtilityOperationReconciler exercises that authority. GitCreateBranch performs the work.
  
5. Success is proven by an accepted branch-reference artifact recording the branch and its resulting commit. The next step binds that artifact and independently verifies that the mounted workspace still represents it.
    - Workspace is mutable, cannot represent durable proof
    - A branch-reference/v1 artifact can

### Artifact
branch-reference/v1
- Repository identity
- Branch name
- Accepted base commit
- Resulting branch commit
- Producing UtilityOperation UID
- Idempotency Identity

### Important Nouns
- repository-revision artifact
- accepted base commit
- branch name
- local branch reference
- workspace
- workspace HEAD
- UtilityOperation
- branch-reference artifact

### Important Verbs
- resolve the accepted commit
- validate the branch name
- create the branch
- validate an existing branch
- attach HEAD
- record the resulting branch identity

### Risks
- creating the branch on the wrong commit
- accepting a mutable branch name without sufficient policy constraints
- colliding with an existing branch
- accepting an existing branch with unexpected commits
- using mutable workspace state instead of accepted artifact evidence
- reporting success without recording the resulting branch commit
- allowing the workspace and branch artifact to diverge before the next step


| Property | Decision |
|---|---|
| Relationship | Accepted repository snapshot → named local line of work |
| Exact subject | Branch X in workflow workspace Z, based on accepted commit Y |
| Requested by | Workflow author through `StepConfig` |
| Base selected by | Accepted repository-revision artifact |
| Branch name selected by | Workflow declaration, subject to operation validation and policy |
| Authority primitive | `UtilityOperation` |
| Status/authority owner | `UtilityOperationReconciler` |
| Execution mechanism | Registered `GitCreateBranch` operation |
| Credential requirement | None for local branch creation |
| Side effect | Local branch reference created or recovered; HEAD attached to it |
| Durable output | `branch-reference/v1` artifact |
| Evidence acceptance | Trusted collector and Artifact controller |
| Next consumer | Subsequent AgentRun or UtilityOperation |
| Retry rule | Existing branch must be proven compatible with the accepted base |
| Cleanup owner | Workflow workspace lifecycle |

### Gaps Exposed:
- Branch step needs to consume the exact repository-revision artifact UID and digest.
- Output evidence is incomplete
  - Does not record branch name, accepted base commit, resulting branch commit
- Workspace state is trusted too heavily
  - Currently the expected branch is still checked out
  - HEAD still matches the accepted branch artifact
  - The workspace belongs to the same workflow and attempt chain
- Existing branch semantics need a deicision
  - Currently the implementation accepts an existing branch when the requested base is an ancestor. It must either:
    - Accept the later commits as long as the base history matches
    - Only accept a branch whose history exactly matches the request