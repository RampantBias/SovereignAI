# CRDs

Generated CRDs live in `config/crd/bases` and are installed from there:

```powershell
kubectl apply -k config/crd/bases
```

This directory exists to document the deployment boundary without duplicating
generated manifests. Keeping generated CRDs under `config/crd/bases` preserves
the default `controller-gen` output path.
