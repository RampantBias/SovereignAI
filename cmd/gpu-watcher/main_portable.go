//go:build !nvidia

package main

import "log"

// GPU telemetry is supplied by DCGM Exporter in portable MVP deployments.
// The legacy NVML observer remains available only with the nvidia build tag
// until its control-plane responsibilities move to inference resources.
func main() {
	log.Print("gpu-watcher is disabled; deploy DCGM Exporter for GPU telemetry")
}
