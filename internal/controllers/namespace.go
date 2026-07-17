package controllers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func namespaceTerminating(ctx context.Context, reader client.Reader, namespace string) (bool, error) {
	if namespace == "" {
		return false, nil
	}
	var current corev1.Namespace
	err := reader.Get(ctx, types.NamespacedName{Name: namespace}, &current)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !current.DeletionTimestamp.IsZero(), nil
}
