# Getting Started

This guide records the startup order for the reduced SovereignAI MVP. The
acceptance contract in [`mvp.md`](mvp.md) is authoritative when this guide and
the MVP contract disagree.

The demo stack is under active construction. PostgreSQL, the registry, and
Argo CD are currently integrated. Add another package to
`deploy/demo/kustomization.yaml` only after that child renders on its own.

Additionally, I will migrate this towards a quick one-liner once I've developed
all of the resources. But for now this works better for as it allows me to 
iterate much more quickly. 

## 1. Host prerequisites

You must supply and configure:

- a Kubernetes or k3s cluster and administrator `kubectl` access
- containerd and, for GPU inference, the NVIDIA host driver and container
  runtime
- a default Kubernetes StorageClass, or an explicitly selected one
- network reachability to a git provider
- any host-level registry mirror or trust configuration required by
  k3s/containerd

The demo stack installs cluster services. It does not install or modify the
host driver, NVIDIA container runtime, containerd, or Kubernetes itself.

Check the current context and storage before installing anything:

```powershell
kubectl config current-context
kubectl version
kubectl get nodes -o wide
kubectl get storageclass
```

Do not continue until the intended context is selected and a usable
StorageClass exists.

## 2. Render before applying

Rendering catches missing files, bad relative paths, and many Kustomize errors
without changing the cluster:

```powershell
kubectl kustomize deploy/demo
```

The command should succeed before installation. Rendering does not prove
that referenced Secrets exist or that an image can run on the target node.
Rendering currently needs network access to retrieve the commit-pinned Argo CD
manifest.

## 3. PostgreSQL audit database

PostgreSQL needs a namespace and a generated credential Secret before its
StatefulSet starts. Kustomize assembles deterministic manifests; it does not
generate a cryptographically random password.

The clean startup order is:

1. Create the namespace.
2. Create the credential Secret from a temporary file outside the repository.
3. Apply the PostgreSQL Kustomize package.
4. Wait for readiness and inspect its PVC.

### 3.1 Create the namespace

```powershell
kubectl apply -f deploy/demo/postgresql/namespace.yaml
```

### 3.2 Create the PostgreSQL Secret

The StatefulSet expects an `Opaque` Secret named
`sovereign-audit-postgresql` in the `sovereign-audit` namespace. It requires
the keys `username` and `password`.

Create a temporary file named `postgresql-credentials.env` outside the
repository:

```text
username=sovereign_audit
password=CHOOSE_A_DEMO_PASSWORD
```

Create or update the Secret from that file:

```console
kubectl create secret generic sovereign-audit-postgresql --namespace sovereign-audit --type=Opaque --from-env-file=/path/to/postgresql-credentials.env --dry-run=client -o yaml | kubectl apply -f -
```

The temporary file keeps the values out of the command and terminal history,
but it is still plaintext. Keep it outside the repository and delete it after
`kubectl` succeeds.

Do not retrieve or print the Secret merely to verify it. Verify only its type
and key names:

```console
kubectl -n sovereign-audit get secret sovereign-audit-postgresql -o go-template='type={{.type}}{{"\n"}}keys={{range $key, $_ := .data}}{{$key}} {{end}}{{"\n"}}'
```

### 3.3 Apply and verify PostgreSQL

```powershell
kubectl apply -k deploy/demo/postgresql
kubectl -n sovereign-audit rollout status statefulset/postgresql --timeout=5m
kubectl -n sovereign-audit get pod,service,pvc
```

The expected persistent claim is named `data-postgresql-0`. The client
endpoint inside the cluster is:

```text
postgresql.sovereign-audit:5432
```

The SovereignAI control-plane package consumes a DSN derived from this
generated credential through a Secret-backed
`SOVEREIGN_AUDIT_DSN`. The controller and API Deployments consume the
`sovereign-audit-client` Secret described below; the DSN is not stored directly
in either Deployment manifest.

### 3.4 Create the audit client Secret

Apply the control-plane Namespace before creating its Secret:

```console
kubectl apply -f deploy/base/namespace.yaml
```

Create a temporary file named `audit-client.env` outside the repository. Use
the same username and password supplied in `postgresql-credentials.env`:

```text
SOVEREIGN_AUDIT_DSN=postgres://sovereign_audit:CHOOSE_A_DEMO_PASSWORD@postgresql.sovereign-audit.svc.cluster.local:5432/sovereign_audit?sslmode=disable
SOVEREIGN_AUDIT_REQUIRED=true
```

For this simple demo flow, choose an alphanumeric PostgreSQL password. If the
password contains URI-reserved characters, percent-encode it in the DSN.

Create or update the client Secret from the file:

```console
kubectl create secret generic sovereign-audit-client --namespace sovereign-orchestrator-system --type=Opaque --from-env-file=/path/to/audit-client.env --dry-run=client -o yaml | kubectl apply -f -
```

Delete the temporary file after `kubectl` succeeds. The PostgreSQL Secret and
the audit client Secret intentionally serve different consumers: PostgreSQL
reads `username` and `password`, while the controller and API read
`SOVEREIGN_AUDIT_DSN` and `SOVEREIGN_AUDIT_REQUIRED`.

Verify only the client Secret's type and key names:

```console
kubectl -n sovereign-orchestrator-system get secret sovereign-audit-client -o go-template='type={{.type}}{{"\n"}}keys={{range $key, $_ := .data}}{{$key}} {{end}}{{"\n"}}'
```

Apply the control plane only after this Secret exists. A missing Secret keeps
the controller and API Pods from starting instead of silently falling back to
process-local audit storage.

Do not use `kubectl delete -k deploy/demo/postgresql` as a data-preserving
uninstall procedure. The package contains its Namespace, and deleting the
Namespace also deletes namespaced PVCs. The ordered uninstall work must remove
the workload while retaining the PVC unless the operator explicitly requests
retained-data deletion.

## 4. Argo CD

Install the checksum-pinned Argo CD package:

```console
bash deploy/linux/install-argocd.sh
```

The installer uses server-side apply for Argo CD's large CRDs, waits for all
Argo CD workloads, and rejects external Service or Ingress exposure.

Access the API/UI only through a loopback port-forward:

```console
kubectl -n argocd port-forward --address 127.0.0.1 service/argocd-server 8443:443
```

Open `https://127.0.0.1:8443`. Initial login and password-rotation directions
are in [`../deploy/demo/argocd/README.md`](../deploy/demo/argocd/README.md).
Do not change the port-forward address to `0.0.0.0` for the reduced demo.

## 5. Calculator Git remote

The calculator runs from an existing external HTTPS Git remote. Git hosting is
not installed by the demo stack.

Create the remote without a README, license, `.gitignore`, or initial commit.
Configure the operator's normal local Git credential helper, then seed it:

```powershell
.\demo\scripts\seed-calculator.ps1 `
    -RemoteUrl "https://github.com/OWNER/calculator-demo.git"
```

The seed script:

- refuses a URL containing credentials;
- refuses a remote that already has any branch;
- pushes exactly one reviewed `main` seed commit; and
- reports the verified full commit SHA.

Record that full SHA in the Project's
`spec.applicationRepository.defaultRevision` and in the frozen calculator
change request. The seed script uses the operator's local Git credential
helper. It does not use the Kubernetes repository Secret created below.

## 6. Repository credential Secret

Trusted utility operations use a Project-owned Kubernetes Secret to reach the
calculator remote. The reduced MVP accepts exactly this shape:

- type `Opaque`;
- exactly one data key named `credentials`; and
- one non-empty Git credential-store entry using HTTPS.

The decoded `credentials` file contains one line:

```text
https://ENCODED_USERNAME:ENCODED_SECRET@git.example.test
```

Percent-encode the username and secret separately as URL components. In
particular, characters such as `@`, `:`, `/`, `%`, `?`, and `#` must not
appear unencoded. Keep the Project repository URL itself credential-free.

After the `sovereign-orchestrator-system` namespace exists, create a temporary
credential file outside the repository and then create the Secret:

```console
kubectl create secret generic calculator-git --namespace sovereign-orchestrator-system --type=Opaque --from-file=credentials=/path/to/temporary-git-credentials --dry-run=client -o yaml | kubectl apply -f -
```

Permanently delete the decoded temporary file after `kubectl` succeeds. Do not
print, log, commit, audit, or include it in an air-gap archive.

Verify only the Secret type and key name:

```console
kubectl -n sovereign-orchestrator-system get secret calculator-git -o go-template='type={{.type}}{{"\n"}}keys={{range $key, $_ := .data}}{{$key}} {{end}}{{"\n"}}'
```

Reference it from the Project:

```yaml
spec:
  applicationRepository:
    url: https://github.com/OWNER/calculator-demo.git
    defaultRevision: FULL_SEED_COMMIT_SHA
    credentialRef:
      namespace: sovereign-orchestrator-system
      name: calculator-git
```

The controller validates the source Secret and projects only the
`credentials` entry into a short-lived trusted utility workload. Agents and
the MCP service do not receive it.

## 7. Other startup credentials

The completed demo will also need the following credential boundaries. Their
exact creation commands belong beside the components that consume them:

| Credential | Owner and lifetime | Repository rule |
| --- | --- | --- |
| Registry push credential | Trusted build utility only; operation-scoped copy | Never give it to an agent, MCP, or validation workload |
| Model-source token, if required | Preload job only; remove after verified preload | Never place it in the model-cache PVC or runtime Pod |
| API and human signing material | API/authentication boundary with documented rotation | Never generate or store private keys in Git |

Do not add placeholder Secret values to Kustomize packages. A component should
reference a documented Secret name and key contract; the ordered installer
creates the actual Secret.
