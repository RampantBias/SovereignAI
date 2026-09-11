package controllers

import (
	"context"
	"fmt"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/validation"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ProjectConditionConfigurationValid      = v1alpha1.ProjectConditionConfigurationValid
	ProjectConditionValidationProviderReady = v1alpha1.ProjectConditionValidationProviderReady
	ProjectFinalizer                        = "sovereign-ai.io/project-validation-cleanup"
	projectDriftInterval                    = 5 * time.Minute

	projectReasonConfigurationValid   = "ConfigurationValid"
	projectReasonInvalidConfiguration = "InvalidConfiguration"

	supportedProjectValidationProvider = "argocd-kustomize"
	maxProjectConditionMessageBytes    = 2048
)

var fullGitSHA1 = regexp.MustCompile(`^[0-9a-f]{40}$`)

type SovereignProjectReconciler struct {
	client.Client
	Provisioner validation.ProjectProvisioner
	Audit       audit.Recorder
	Now         func() time.Time
}

func (r *SovereignProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SovereignProject{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.projectsReferencingSecret),
		).
		Watches(
			&v1alpha1.PolicyProfile{},
			handler.EnqueueRequestsFromMapFunc(r.projectsReferencingPolicy),
		).
		Complete(r)
}

func (r *SovereignProjectReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var project v1alpha1.SovereignProject
	if err := r.Get(ctx, request.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !project.DeletionTimestamp.IsZero() {
		return r.cleanupValidationProject(ctx, &project)
	}

	if err := r.updateConfigurationStatus(ctx, request.NamespacedName); err != nil {
		return ctrl.Result{}, err
	}
	// Refresh the spec and resource version after updating configuration status.
	if err := r.Get(ctx, request.NamespacedName, &project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !project.DeletionTimestamp.IsZero() {
		return r.cleanupValidationProject(ctx, &project)
	}
	if len(ValidateSovereignProjectSpec(&project)) > 0 {
		return ctrl.Result{}, r.updateValidationProviderStatus(ctx, &project, project.Status.ValidationProviderRef, metav1.ConditionFalse, "InvalidConfiguration", "Project configuration must be valid before provisioning validation")
	}
	if r.Provisioner == nil {
		return ctrl.Result{RequeueAfter: projectDriftInterval}, r.updateValidationProviderStatus(ctx, &project, project.Status.ValidationProviderRef, metav1.ConditionFalse, "ProviderUnavailable", "Validation project provisioner is not configured")
	}
	if !controllerutil.ContainsFinalizer(&project, ProjectFinalizer) {
		controllerutil.AddFinalizer(&project, ProjectFinalizer)
		return ctrl.Result{}, r.Update(ctx, &project)
	}
	reference, err := r.Provisioner.EnsureProject(ctx, validationProjectRequest(&project))
	if err != nil {
		reason := "AppProjectProvisioningFailed"
		if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
			reason = "AppProjectConflict"
		}
		if statusErr := r.updateValidationProviderStatus(ctx, &project, project.Status.ValidationProviderRef, metav1.ConditionFalse, reason, err.Error()); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}
	if reference == "" {
		return ctrl.Result{}, fmt.Errorf("validation provisioner returned an empty AppProject reference")
	}
	return ctrl.Result{RequeueAfter: projectDriftInterval}, r.updateValidationProviderStatus(ctx, &project, reference, metav1.ConditionTrue, "AppProjectReady", "Argo AppProject policy is provisioned")
}

func validationProjectRequest(project *v1alpha1.SovereignProject) validation.ProjectRequest {
	return validation.ProjectRequest{Name: project.Name, UID: project.UID, InfrastructureRepo: project.Spec.Validation.InfrastructureRepo}
}

func (r *SovereignProjectReconciler) cleanupValidationProject(ctx context.Context, project *v1alpha1.SovereignProject) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(project, ProjectFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.updateValidationProviderStatus(ctx, project, project.Status.ValidationProviderRef, metav1.ConditionFalse, "ProjectTerminating", "Waiting for referencing Applications and AppProject cleanup; workflow environments are not deleted automatically"); err != nil {
		return ctrl.Result{}, err
	}
	if r.Provisioner == nil {
		return ctrl.Result{}, fmt.Errorf("validation project provisioner is unavailable for cleanup")
	}
	done, err := r.Provisioner.DestroyProject(ctx, validationProjectRequest(project))
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	// Read again after the status write; remove only our finalizer.
	var latest v1alpha1.SovereignProject
	if err := r.Get(ctx, client.ObjectKeyFromObject(project), &latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if latest.UID != project.UID {
		return ctrl.Result{}, fmt.Errorf("project identity changed during cleanup")
	}
	controllerutil.RemoveFinalizer(&latest, ProjectFinalizer)
	return ctrl.Result{}, r.Update(ctx, &latest)
}

func (r *SovereignProjectReconciler) updateValidationProviderStatus(ctx context.Context, project *v1alpha1.SovereignProject, reference string, status metav1.ConditionStatus, reason, message string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.SovereignProject
		if err := r.Get(ctx, client.ObjectKeyFromObject(project), &latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if latest.UID != project.UID || latest.Generation != project.Generation {
			return fmt.Errorf("project changed while provisioning validation; reconcile the current generation")
		}
		if status == metav1.ConditionTrue && !latest.DeletionTimestamp.IsZero() {
			return fmt.Errorf("project is terminating")
		}
		if len(message) > maxProjectConditionMessageBytes {
			message = message[:maxProjectConditionMessageBytes]
		}
		condition := metav1.Condition{Type: ProjectConditionValidationProviderReady, Status: status, Reason: reason, Message: message, ObservedGeneration: project.Generation, LastTransitionTime: r.now()}
		if latest.Status.ValidationProviderRef == reference && conditionsEqual(apiMeta.FindStatusCondition(latest.Status.Conditions, condition.Type), condition) {
			return nil
		}
		latest.Status.ValidationProviderRef = reference
		apiMeta.SetStatusCondition(&latest.Status.Conditions, condition)
		return r.Status().Update(ctx, &latest)
	})
}

// ValidateSovereignProjectSpec validates the Project-owned authority that the
// current API can represent. Dependency existence and readiness are reconciled
// separately from desired-state validity.
func ValidateSovereignProjectSpec(project *v1alpha1.SovereignProject) field.ErrorList {
	if project == nil {
		return field.ErrorList{field.Required(field.NewPath("project"), "Project is required")}
	}

	var errors field.ErrorList
	specPath := field.NewPath("spec")
	// This name is used verbatim in workflow namespace prefixes and Argo labels.
	for _, problem := range k8svalidation.IsDNS1123Label(project.Name) {
		errors = append(errors, field.Invalid(field.NewPath("metadata", "name"), project.Name, problem))
	}

	// validate tenant and policy profile names
	if err := validateTenant(project.Spec.Tenant, specPath.Child("tenant")); err != nil {
		errors = append(errors, err)
	}
	if err := validatePolicyProfileName(project.Spec.PolicyProfileRef, specPath.Child("policyProfileRef")); err != nil {
		errors = append(errors, err)
	}

	errors = append(
		errors,
		validateApplicationRepository(
			project.Spec.ApplicationRepository,
			specPath.Child("applicationRepository"),
		)...,
	)
	errors = append(
		errors,
		validateJobTemplate(
			project.Spec.TestJob,
			specPath.Child("testJob"),
			credentialForbidden,
		)...,
	)
	errors = append(
		errors,
		validateJobTemplate(
			project.Spec.BuildJob,
			specPath.Child("buildJob"),
			credentialOptional,
		)...,
	)
	errors = append(
		errors,
		validateProjectValidation(
			project.Spec.Validation,
			specPath.Child("validation"),
		)...,
	)
	errors = append(
		errors,
		validateRetention(project.Spec.Retention, specPath.Child("retention"))...,
	)

	return errors
}

func validateTenant(tenant string, path *field.Path) *field.Error {
	if tenant == "" {
		return field.Required(path, "tenant is required")
	}
	if strings.TrimSpace(tenant) != tenant {
		return field.Invalid(path, tenant, "must not contain surrounding whitespace")
	}
	if problems := k8svalidation.IsDNS1123Label(tenant); len(problems) > 0 {
		return field.Invalid(path, tenant, strings.Join(problems, "; "))
	}
	return nil
}

func validatePolicyProfileName(name string, path *field.Path) *field.Error {
	if name == "" {
		return field.Required(path, "PolicyProfile name is required")
	}
	if strings.TrimSpace(name) != name {
		return field.Invalid(path, name, "must not contain surrounding whitespace")
	}
	if problems := k8svalidation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return field.Invalid(path, name, strings.Join(problems, "; "))
	}
	return nil
}

func validateApplicationRepository(repository v1alpha1.RepositorySpec, path *field.Path) field.ErrorList {
	var errors field.ErrorList

	errors = append(errors, validateHTTPSRepositoryURL(repository.URL, path.Child("url"))...)

	revisionPath := path.Child("defaultRevision")
	if repository.DefaultRevision == "" {
		errors = append(errors, field.Required(revisionPath, "immutable Git commit is required"))
	} else if !fullGitSHA1.MatchString(repository.DefaultRevision) {
		errors = append(errors, field.Invalid(
			revisionPath,
			repository.DefaultRevision,
			"must be a full lowercase 40-character Git commit SHA",
		))
	}

	errors = append(
		errors,
		validateNamespacedReference(
			repository.CredentialRef,
			path.Child("credentialRef"),
			credentialRequired,
		)...,
	)

	return errors
}

func validateHTTPSRepositoryURL(rawURL string, path *field.Path) field.ErrorList {
	var errors field.ErrorList

	if rawURL == "" {
		return field.ErrorList{field.Required(path, "HTTPS repository URL is required")}
	}
	if strings.TrimSpace(rawURL) != rawURL {
		return field.ErrorList{field.Invalid(path, rawURL, "must not contain surrounding whitespace")}
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return field.ErrorList{field.Invalid(path, rawURL, "must be a valid URL")}
	}

	if parsed.Opaque != "" || !parsed.IsAbs() {
		errors = append(errors, field.Invalid(path, rawURL, "must be an absolute hierarchical URL"))
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		errors = append(errors, field.Invalid(path, rawURL, "scheme must be HTTPS"))
	}
	if parsed.Hostname() == "" {
		errors = append(errors, field.Invalid(path, rawURL, "host is required"))
	}
	if parsed.User != nil {
		errors = append(errors, field.Invalid(path, rawURL, "must not contain embedded credentials"))
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		errors = append(errors, field.Invalid(path, rawURL, "must not contain a query"))
	}
	if parsed.Fragment != "" {
		errors = append(errors, field.Invalid(path, rawURL, "must not contain a fragment"))
	}
	if parsed.EscapedPath() == "" || parsed.EscapedPath() == "/" {
		errors = append(errors, field.Invalid(path, rawURL, "repository path is required"))
	}

	return errors
}

type credentialRequirement int

const (
	credentialOptional credentialRequirement = iota
	credentialRequired
	credentialForbidden
)

func validateJobTemplate(template v1alpha1.JobTemplateSpec, path *field.Path, credentialRule credentialRequirement) field.ErrorList {
	var errors field.ErrorList

	imagePath := path.Child("image")
	if template.Image == "" {
		errors = append(errors, field.Required(imagePath, "container image is required"))
	} else if strings.TrimSpace(template.Image) != template.Image || containsWhitespace(template.Image) {
		errors = append(errors, field.Invalid(imagePath, template.Image, "must not contain whitespace"))
	}

	commandPath := path.Child("command")
	if len(template.Command) == 0 {
		errors = append(errors, field.Required(commandPath, "at least one command element is required"))
	}
	for index, element := range template.Command {
		elementPath := commandPath.Index(index)
		if element == "" {
			errors = append(errors, field.Required(elementPath, "command element must not be empty"))
		} else if strings.IndexByte(element, 0) >= 0 {
			errors = append(errors, field.Invalid(elementPath, element, "must not contain a NUL byte"))
		}
	}
	for index, argument := range template.Args {
		if strings.IndexByte(argument, 0) >= 0 {
			errors = append(errors, field.Invalid(path.Child("args").Index(index), argument, "must not contain a NUL byte"))
		}
	}

	errors = append(
		errors,
		validateNamespacedReference(template.CredentialRef, path.Child("credentialRef"), credentialRule)...,
	)

	return errors
}

func validateNamespacedReference(reference v1alpha1.NamespacedReference, path *field.Path, requirement credentialRequirement) field.ErrorList {
	var errors field.ErrorList
	hasNamespace := reference.Namespace != ""
	hasName := reference.Name != ""

	if requirement == credentialForbidden {
		if hasNamespace || hasName {
			errors = append(errors, field.Forbidden(path, "this operation does not accept a credential reference"))
		}
		return errors
	}
	if requirement == credentialOptional && !hasNamespace && !hasName {
		return errors
	}

	namespacePath := path.Child("namespace")
	if !hasNamespace {
		errors = append(errors, field.Required(namespacePath, "credential namespace is required"))
	} else {
		for _, message := range k8svalidation.IsDNS1123Label(reference.Namespace) {
			errors = append(errors, field.Invalid(namespacePath, reference.Namespace, message))
		}
	}

	namePath := path.Child("name")
	if !hasName {
		errors = append(errors, field.Required(namePath, "credential Secret name is required"))
	} else {
		for _, message := range k8svalidation.IsDNS1123Subdomain(reference.Name) {
			errors = append(errors, field.Invalid(namePath, reference.Name, message))
		}
	}

	return errors
}

func validateProjectValidation(validation v1alpha1.ProjectValidationSpec, path *field.Path) field.ErrorList {
	var errors field.ErrorList

	providerPath := path.Child("provider")
	if validation.Provider == "" {
		errors = append(errors, field.Required(providerPath, "validation provider is required"))
	} else if validation.Provider != supportedProjectValidationProvider {
		errors = append(errors, field.NotSupported(
			providerPath,
			validation.Provider,
			[]string{supportedProjectValidationProvider},
		))
	}

	errors = append(
		errors,
		validateHTTPSRepositoryURL(
			validation.InfrastructureRepo,
			path.Child("infrastructureRepository"),
		)...,
	)

	overlayPath := path.Child("overlayPath")
	switch {
	case validation.OverlayPath == "":
		errors = append(errors, field.Required(overlayPath, "validation overlay path is required"))
	case strings.TrimSpace(validation.OverlayPath) != validation.OverlayPath:
		errors = append(errors, field.Invalid(overlayPath, validation.OverlayPath, "must not contain surrounding whitespace"))
	case strings.Contains(validation.OverlayPath, `\`):
		errors = append(errors, field.Invalid(overlayPath, validation.OverlayPath, "must use forward slashes"))
	case pathpkg.IsAbs(validation.OverlayPath):
		errors = append(errors, field.Invalid(overlayPath, validation.OverlayPath, "must be relative"))
	case pathpkg.Clean(validation.OverlayPath) != validation.OverlayPath ||
		validation.OverlayPath == "." ||
		strings.HasPrefix(validation.OverlayPath, "../"):
		errors = append(errors, field.Invalid(overlayPath, validation.OverlayPath, "must be a clean relative path without parent traversal"))
	}

	errors = append(errors, validateValidationImageName(validation.ImageName, path.Child("imageName"))...)
	if validation.ImageSelector != "" {
		errors = append(errors, validateValidationImageName(validation.ImageSelector, path.Child("imageSelector"))...)
	}

	return errors
}

func validateValidationImageName(imageName string, path *field.Path) field.ErrorList {
	switch {
	case imageName == "":
		return field.ErrorList{field.Required(path, "calculator image name is required")}
	case strings.TrimSpace(imageName) != imageName || containsWhitespace(imageName):
		return field.ErrorList{field.Invalid(path, imageName, "must not contain whitespace")}
	case strings.ToLower(imageName) != imageName:
		return field.ErrorList{field.Invalid(path, imageName, "must be lowercase")}
	case strings.Contains(imageName, "@"):
		return field.ErrorList{field.Invalid(path, imageName, "must be an image name, not a digest reference")}
	case strings.HasPrefix(imageName, "/") ||
		strings.HasSuffix(imageName, "/") ||
		strings.Contains(imageName, "//"):
		return field.ErrorList{field.Invalid(path, imageName, "must contain non-empty path components")}
	default:
		return nil
	}
}

func validateRetention(retention v1alpha1.RetentionSpec, path *field.Path) field.ErrorList {
	var errors field.ErrorList

	if retention.CompletedWorkflowTTL != nil && retention.CompletedWorkflowTTL.Duration <= 0 {
		errors = append(errors, field.Invalid(
			path.Child("completedWorkflowTTL"),
			retention.CompletedWorkflowTTL.Duration.String(),
			"must be greater than zero",
		))
	}
	if retention.FailedWorkflowTTL != nil && retention.FailedWorkflowTTL.Duration <= 0 {
		errors = append(errors, field.Invalid(
			path.Child("failedWorkflowTTL"),
			retention.FailedWorkflowTTL.Duration.String(),
			"must be greater than zero",
		))
	}

	return errors
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}

func (r *SovereignProjectReconciler) updateConfigurationStatus(ctx context.Context, key types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var project v1alpha1.SovereignProject
		if err := r.Get(ctx, key, &project); err != nil {
			return client.IgnoreNotFound(err)
		}
		if !project.DeletionTimestamp.IsZero() {
			return nil
		}

		validationErrors := ValidateSovereignProjectSpec(&project)
		condition := metav1.Condition{
			Type:               ProjectConditionConfigurationValid,
			Status:             metav1.ConditionTrue,
			Reason:             projectReasonConfigurationValid,
			Message:            "Project configuration is valid",
			ObservedGeneration: project.Generation,
			LastTransitionTime: r.now(),
		}
		if len(validationErrors) > 0 {
			condition.Status = metav1.ConditionFalse
			condition.Reason = projectReasonInvalidConfiguration
			condition.Message = projectValidationConditionMessage(validationErrors)
		}

		existing := apiMeta.FindStatusCondition(
			project.Status.Conditions,
			ProjectConditionConfigurationValid,
		)
		if project.Status.ObservedGeneration == project.Generation &&
			conditionsEqual(existing, condition) {
			return nil
		}

		project.Status.ObservedGeneration = project.Generation
		apiMeta.SetStatusCondition(&project.Status.Conditions, condition)
		return r.Status().Update(ctx, &project)
	})
}

func conditionsEqual(existing *metav1.Condition, desired metav1.Condition) bool {
	return existing != nil &&
		existing.Status == desired.Status &&
		existing.Reason == desired.Reason &&
		existing.Message == desired.Message &&
		existing.ObservedGeneration == desired.ObservedGeneration
}

func projectValidationConditionMessage(errors field.ErrorList) string {
	messages := make([]string, 0, len(errors))
	for _, validationError := range errors {
		if validationError == nil {
			continue
		}
		message := validationError.Field
		if validationError.Detail != "" {
			message += ": " + validationError.Detail
		}
		messages = append(messages, message)
	}

	message := strings.Join(messages, "; ")
	if len(message) <= maxProjectConditionMessageBytes {
		return message
	}
	return message[:maxProjectConditionMessageBytes]
}

func (r *SovereignProjectReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now().UTC())
	}
	return metav1.Now()
}

func (r *SovereignProjectReconciler) appendProjectEvent(
	ctx context.Context,
	project *v1alpha1.SovereignProject,
	eventType, action, target, outcome, reason string,
	references map[string]string,
	data any,
) error {
	return audit.AppendControllerEvent(ctx, r.Audit, "sovereignproject-controller", r.Now, audit.EventOptions{
		Type:          eventType,
		Subject:       audit.Subject{Project: project.Name},
		Action:        action,
		Target:        target,
		Outcome:       outcome,
		Reason:        reason,
		CorrelationID: string(project.UID),
		References:    references,
		Data:          data,
	})
}

func (r *SovereignProjectReconciler) projectsReferencingSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var projects v1alpha1.SovereignProjectList
	if err := r.List(ctx, &projects); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to list Projects for Secret watch")
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, project := range projects.Items {
		if referencesSecret(project, secret.Namespace, secret.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: project.Name},
			})
		}
	}
	return requests
}

func referencesSecret(project v1alpha1.SovereignProject, namespace, name string) bool {
	return (project.Spec.ApplicationRepository.CredentialRef.Name == name &&
		project.Spec.ApplicationRepository.CredentialRef.Namespace == namespace) ||
		(project.Spec.BuildJob.CredentialRef.Name == name &&
			project.Spec.BuildJob.CredentialRef.Namespace == namespace) ||
		(project.Spec.TestJob.CredentialRef.Name == name &&
			project.Spec.TestJob.CredentialRef.Namespace == namespace)
}

func (r *SovereignProjectReconciler) projectsReferencingPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	profile, ok := obj.(*v1alpha1.PolicyProfile)
	if !ok {
		return nil
	}

	var projects v1alpha1.SovereignProjectList
	if err := r.List(ctx, &projects); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to list Projects for PolicyProfile watch")
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, project := range projects.Items {
		if project.Spec.PolicyProfileRef == profile.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: project.Name},
			})
		}
	}
	return requests
}
