# 8. Build Image

Build image relies on using a local unsecured registry for image build/push. Ideally, build would be delegated to the github runner
and an image pushed to a secure repository, to be pulled by argocd. I've stayed with a local insecure registry option as I just
don't have the bandwith to make the changes necessary for that.

In my /etc/rancher/k3s/registries.yaml:

```yaml
configs:
  "{your-controlplane-ip}:30500":
    tls:
      insecure_skip_verify: true
```