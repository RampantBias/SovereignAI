package kube

import "k8s.io/client-go/kubernetes"

// Abstraction of KubeClient
type KubeManager struct {
	clientset kubernetes.Interface
}
