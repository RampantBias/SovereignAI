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
	WorkspaceWriterFinalizer        = "sovereign-ai.io/workspace-writer-release"
	AnnotationWorkspaceWriterLease  = "sovereign-ai.io/workspace-writer-lease"
	AnnotationWorkspaceWriterHolder = "sovereign-ai.io/workspace-writer-holder"
	AnnotationWorkspaceWriterEpoch  = "sovereign-ai.io/workspace-writer-epoch"
)

type workspaceWriterState string

const (
	workspaceWriterGranted workspaceWriterState = "Granted"
	workspaceWriterBlocked workspaceWriterState = "Blocked"
	workspaceWriterLost    workspaceWriterState = "Lost"
)

type workspaceWriterGrant struct {
	LeaseName      string
	HolderIdentity string
	Epoch          int32
}

// acquireWorkspaceWriter uses the Lease resourceVersion as the compare-and-
// swap boundary. LeaseTransitions is the monotonically increasing writer
// epoch: It advances only when an empty lease is granted to a new writer.
//
// A caller that has already recorded an epoch may only recover the exact same
// lease term. It must never silently reacquire an empty lease because its old
// pod could still be writing with the previous epoch.
func acquireWorkspaceWriter(ctx context.Context, c client.Client, workflow client.Object, leaseName, kind string, object client.Object, expectedEpoch int32, now time.Time) (workspaceWriterGrant, workspaceWriterState, error) {
	workflowNamespace := workflow.GetNamespace()
	holder, err := workspaceWriterIdentity(kind, object)
	if err != nil {
		return workspaceWriterGrant{}, workspaceWriterBlocked, err
	}
	grant := workspaceWriterGrant{LeaseName: leaseName, HolderIdentity: holder}
	state := workspaceWriterBlocked
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
				state = workspaceWriterGranted
			} else {
				state = workspaceWriterLost
			}
			return nil
		}
		if currentHolder == holder {
			if epoch < 1 {
				return fmt.Errorf("workspace writer lease %s/%s has holder without a positive epoch", workflowNamespace, leaseName)
			}
			grant.Epoch, state = epoch, workspaceWriterGranted
			return nil
		}
		if currentHolder != "" {
			state = workspaceWriterBlocked
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
		grant.Epoch, state = epoch, workspaceWriterGranted
		return nil
	})
	return grant, state, err
}

// releaseWorkspaceWriter clears only the exact holder and epoch that the
// execution unit was granted. The transitions counter is deliberately kept so
// the next successful acquisition receives a strictly greater writer epoch.
func releaseWorkspaceWriter(ctx context.Context, c client.Client, namespace string, grant workspaceWriterGrant, now time.Time) error {
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

func workspaceWriterIdentity(kind string, object client.Object) (string, error) {
	if object.GetUID() == "" {
		return "", fmt.Errorf("%s %s/%s has no UID and cannot hold workspace write authority", kind, object.GetNamespace(), object.GetName())
	}
	return strings.Join([]string{kind, object.GetNamespace(), object.GetName(), string(object.GetUID())}, "/"), nil
}

func workspaceWriterLeaseName(workflowName string) string {
	const suffix = "-workspace-writer"
	maximumPrefix := 63 - len(suffix)
	prefix := strings.Trim(workflowName, "-")
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "-")
	}
	return prefix + suffix
}

func workspaceWriterAnnotations(grant workspaceWriterGrant) map[string]string {
	return map[string]string{
		AnnotationWorkspaceWriterLease:  grant.LeaseName,
		AnnotationWorkspaceWriterHolder: grant.HolderIdentity,
		AnnotationWorkspaceWriterEpoch:  strconv.FormatInt(int64(grant.Epoch), 10),
	}
}

func workspaceWriterEnv(grant workspaceWriterGrant) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "SOVEREIGN_WORKSPACE_WRITER_LEASE", Value: grant.LeaseName},
		{Name: "SOVEREIGN_WORKSPACE_WRITER_HOLDER", Value: grant.HolderIdentity},
		{Name: "SOVEREIGN_WORKSPACE_WRITER_EPOCH", Value: strconv.FormatInt(int64(grant.Epoch), 10)},
	}
}

func podWriterQuiescent(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
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

func jobWriterQuiescent(ctx context.Context, c client.Client, namespace, name string) (bool, error) {
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
