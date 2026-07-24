# SovereignAI calculator demo

The calculator source under `demo/calculator` is tracked by the SovereignAI
repository. The live demonstration uses a separate, pre-created HTTPS Git
remote whose repository is empty until the one-time seed procedure runs.

## Getting Started

Create the remote repository without a README, license, `.gitignore`, or any
other initial commit. Configure the operator's ordinary Git credential helper
for that host, then run:

```powershell
.\demo\scripts\seed-calculator.ps1 `
    -RemoteUrl "https://github.com/OWNER/calculator-demo.git"
```

Must be using a compatible Git version (verified with Git 2.24.1)
## *NOTE*: Add Git versions for compatibility


### Powershell Script:
- accepts only a clean HTTPS URL without embedded credentials
- fails when the remote cannot be queried
- refuses the remote when any branch already exists
- refuses uncommitted calculator seed changes
- copies only files tracked under `demo/calculator`
- creates one `main` seed commit in a temporary repository
- rechecks the remote immediately before a non-force push
- verifies that the remote has exactly one branch and that `main` resolves to
  the local seed commit
- removes its temporary repository on every exit path

The result reports only the clean remote URL, branch, and verified commit SHA.
Record the full SHA as the initial Project revision and as `sourceCommit` in
`demo/change-requests/calculator-divide.v1.json`. A second invocation must fail
because the remote now contains `main`.

The seed script uses the your local Git credential helper. It does not
read the Kubernetes Secret described below. 

## Repository credential Secret

The reduced MVP accepts exactly one repository credential shape:

- Kubernetes Secret type `Opaque`;
- exactly one data key named `credentials`; and
- one non-empty Git credential-store entry using HTTPS.

The decoded file has this form:

```text
https://ENCODED_USERNAME:ENCODED_SECRET@git.example.test
```

The username and secret are URL components. Percent-encode each separately;
characters such as `@`, `:`, `/`, `%`, `?`, and `#` must not appear
unencoded. The Project repository URL itself must remain credential-free.

You can create the secret with:
```kubectl
kubectl create secret generic calculator-git `
    --namespace sovereign-orchestrator-system `
    --from-file="credentials=C:\path\to\temporary-git-credentials"
```
Then permanently delete the decoded file. This workflow is good enough
for an MVP demo.

Reference the Secret from the Project:

```yaml
spec:
  applicationRepository:
    url: https://github.com/OWNER/calculator-demo.git
    defaultRevision: FULL_SEED_COMMIT_SHA
    credentialRef:
      namespace: sovereign-orchestrator-system
      name: calculator-git
```

The controller validates the source Secret before creating an immutable,
operation-scoped copy. Only the `credentials` key is projected into the trusted
utility container. Audit events record only safe identity such as Secret
namespace, name, UID, and credential class.
