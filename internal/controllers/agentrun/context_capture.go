package agentrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// The pod receives a run-scoped upload credential and public CA, never a database DSN.
func (r *AgentRunReconciler) ensureContextCapture(ctx context.Context, run *v1alpha1.AgentRun) (*agentcontract.ContextCapture, error) {
	if r.ContextAPIAddress == "" {
		return nil, nil
	}
	name := agentcontract.ContextCredentialName(run.Name)
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: name}, &existing)
	if apierrors.IsNotFound(err) {
		var source corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.ContextTLSNamespace, Name: r.ContextTLSSecret}, &source); err != nil {
			return nil, fmt.Errorf("read context API trust certificate: %w", err)
		}
		ca := source.Data["ca.crt"]
		if len(ca) == 0 {
			ca = source.Data["tls.crt"]
		}
		if len(ca) == 0 {
			return nil, fmt.Errorf("context API certificate is missing")
		}
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			return nil, err
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace}, Data: map[string][]byte{"token": []byte(hex.EncodeToString(token)), "ca.crt": ca}}
		if err := controllerutil.SetControllerReference(run, secret, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		err = r.Get(ctx, client.ObjectKeyFromObject(secret), &existing)
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(&existing, run) || len(existing.Data["token"]) != 64 || len(existing.Data["ca.crt"]) == 0 {
		return nil, fmt.Errorf("invalid context upload credential for AgentRun %s", run.Name)
	}
	return &agentcontract.ContextCapture{Endpoint: r.ContextAPIAddress, Namespace: run.Namespace, AgentRun: run.Name, AgentRunUID: string(run.UID), CredentialPath: "/context-upload/token", CAPath: "/context-upload/ca.crt"}, nil
}
