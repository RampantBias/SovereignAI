# NVIDIA device plugin

This is the retained connected-demo device-plugin package. The NVIDIA host
driver, Container Toolkit, containerd configuration, and `nvidia`
RuntimeClass are prerequisites and are not installed here.

The DaemonSet deliberately sets `FAIL_ON_INIT_ERROR=true`; a missing host
runtime must fail visibly instead of producing a misleading ready demo.
