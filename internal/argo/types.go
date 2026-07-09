package argo

import "k8s.io/client-go/dynamic"

type ClientConfig struct {
	ServerAddr string
	Token      string
	Insecure   bool
}

type argoManager struct {
	client    dynamic.Interface
	config    ClientConfig
	Namespace string
}
