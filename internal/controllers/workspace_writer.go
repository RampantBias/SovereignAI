package controllers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	WorkspaceWriterFinalizer              = "sovereign-ai.io/workspace-writer-release"
	AnnotationWorkspaceWriterLease        = "sovereign-ai.io/workspace-writer-lease"
	AnnotationWorkspaceWriterHolder       = "sovereign-ai.io/workspace-writer-holder"
	AnnotationWorkspaceWriterEpoch        = "sovereign-ai.io/workspace-writer-epoch"
	workspaceWorkloadID             int64 = 65532
)

type WorkspaceWriterState string

const (
	WorkspaceWriterGranted WorkspaceWriterState = "Granted"
	WorkspaceWriterBlocked WorkspaceWriterState = "Blocked"
	WorkspaceWriterLost    WorkspaceWriterState = "Lost"
)

type WorkspaceWriterGrant struct {
	LeaseName      string
	HolderIdentity string
	Epoch          int32
}

func WorkspaceWorkloadSecurityContext() *corev1.PodSecurityContext {
	nonRoot, identity := true, workspaceWorkloadID
	return &corev1.PodSecurityContext{
		RunAsNonRoot: &nonRoot,
		RunAsUser:    &identity,
		RunAsGroup:   &identity,
		FSGroup:      &identity,
	}
}

// AcquireWorkspaceWriter uses the Lease resourceVersion as the compare-and-
// swap boundary. LeaseTransitions is the monotonically increasing writer
// epoch: It advances only when an empty lease is granted to a new writer.
//
// A caller that has already recorded an epoch may only recover the exact same
// lease term. It must never silently reacquire an empty lease because its old
// pod could still be writing with the previous epoch.
func AcquireWorkspaceWriter(ctx context.Context, c client.Client, workflow client.Object, leaseName, kind string, object client.Object, expectedEpoch int32, now time.Time) (WorkspaceWriterGrant, WorkspaceWriterState, error) {
	workflowNamespace := workflow.GetNamespace()
	holder, err := WorkspaceWriterIdentity(kind, object)
	if err != nil {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
	}
	grant := WorkspaceWriterGrant{LeaseName: leaseName, HolderIdentity: holder}
	state := WorkspaceWriterBlocked
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var lease coordinationv1.Lease
		if err := c.Get(ctx, types.NamespacedName{Namespace: workflowNamespace, Name: leaseName}, &lease); err != nil {
			return err
		}
		if !metav1.IsControlledBy(&lease, workflow) {
			return fmt.Errorf("workspace writer lease %s/%s is not controlled by workflow %s", workflowNamespace, leaseName, workflow.GetName())
		}
		currentHolder := ""
		if lease.Spec.HolderIdentity != nil {
			currentHolder = *lease.Spec.HolderIdentity
		}
		epoch := int32(0)
		if lease.Spec.LeaseTransitions != nil {
			epoch = *lease.Spec.LeaseTransitions
		}

		if expectedEpoch > 0 {
			grant.Epoch = expectedEpoch
			if currentHolder == holder && epoch == expectedEpoch {
				state = WorkspaceWriterGranted
			} else {
				state = WorkspaceWriterLost
			}
			return nil
		}
		if currentHolder == holder {
			if epoch < 1 {
				return fmt.Errorf("workspace writer lease %s/%s has holder without a positive epoch", workflowNamespace, leaseName)
			}
			grant.Epoch, state = epoch, WorkspaceWriterGranted
			return nil
		}
		if currentHolder != "" {
			state = WorkspaceWriterBlocked
			return nil
		}
		if epoch == math.MaxInt32 {
			return fmt.Errorf("workspace writer epoch exhausted for lease %s/%s", workflowNamespace, leaseName)
		}
		epoch++
		acquired := metav1.NewMicroTime(now.UTC())
		lease.Spec.HolderIdentity = &holder
		lease.Spec.AcquireTime = &acquired
		lease.Spec.RenewTime = &acquired
		lease.Spec.LeaseTransitions = &epoch
		if err := c.Update(ctx, &lease); err != nil {
			return err
		}
		grant.Epoch, state = epoch, WorkspaceWriterGranted
		return nil
	})
	return grant, state, err
}

// ReleaseWorkspaceWriter clears only the exact holder and epoch that the
// execution unit was granted. The transitions counter is deliberately kept so
// the next successful acquisition receives a strictly greater writer epoch.
func ReleaseWorkspaceWriter(ctx context.Context, c client.Client, namespace string, grant WorkspaceWriterGrant, now time.Time) error {
	if grant.LeaseName == "" || grant.HolderIdentity == "" || grant.Epoch < 1 {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var lease coordinationv1.Lease
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: grant.LeaseName}, &lease); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !lease.DeletionTimestamp.IsZero() {
			return nil
		}
		currentHolder := ""
		if lease.Spec.HolderIdentity != nil {
			currentHolder = *lease.Spec.HolderIdentity
		}
		epoch := int32(0)
		if lease.Spec.LeaseTransitions != nil {
			epoch = *lease.Spec.LeaseTransitions
		}
		if currentHolder == "" || currentHolder != grant.HolderIdentity || epoch != grant.Epoch {
			return nil
		}
		renewed := metav1.NewMicroTime(now.UTC())
		empty := ""
		lease.Spec.HolderIdentity = &empty
		lease.Spec.AcquireTime = nil
		lease.Spec.RenewTime = &renewed
		return c.Update(ctx, &lease)
	})
}

func WorkspaceWriterIdentity(kind string, object client.Object) (string, error) {
	if object.GetUID() == "" {
		return "", fmt.Errorf("%s %s/%s has no UID and cannot hold workspace write authority", kind, object.GetNamespace(), object.GetName())
	}
	return strings.Join([]string{kind, object.GetNamespace(), object.GetName(), string(object.GetUID())}, "/"), nil
}

func WorkspaceWriterAnnotations(grant WorkspaceWriterGrant) map[string]string {
	return map[string]string{
		AnnotationWorkspaceWriterLease:  grant.LeaseName,
		AnnotationWorkspaceWriterHolder: grant.HolderIdentity,
		AnnotationWorkspaceWriterEpoch:  strconv.FormatInt(int64(grant.Epoch), 10),
	}
}

func WorkspaceWriterEnv(grant WorkspaceWriterGrant) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "SOVEREIGN_WORKSPACE_WRITER_LEASE", Value: grant.LeaseName},
		{Name: "SOVEREIGN_WORKSPACE_WRITER_HOLDER", Value: grant.HolderIdentity},
		{Name: "SOVEREIGN_WORKSPACE_WRITER_EPOCH", Value: strconv.FormatInt(int64(grant.Epoch), 10)},
	}
}

func PodWriterQuiescent(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
	if name == "" {
		return true, nil
	}
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed, nil
}

func JobWriterQuiescent(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
	if name == "" {
		return true, nil
	}
	var job batchv1.Job
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return job.Status.Active == 0 && (job.Status.Succeeded > 0 || job.Status.Failed > 0), nil
}
