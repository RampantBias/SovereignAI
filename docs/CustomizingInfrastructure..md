To customize any of the packages included (Istio/Argo/etc.) you should complete the following to ensure ArgoCD does not overwrite your manual changes:
1. Fork the repository*
2. Modify the values.yaml for the associated package.
3. Update the orchestrator's infrastructure URL to use your new repository link.


* You can use an offline repository as well. Information on 