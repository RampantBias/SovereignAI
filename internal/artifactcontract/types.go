package artifactcontract

const (
	ChangeRequestContract      = "change-request/v1"
	RepositoryRevisionContract = "repository-revision/v1"
	ImplementationPlanContract = "implementation-plan/v1"
	ChangeSetContract          = "change-set/v1"
	PreparedCandidateContract  = "prepared-candidate/v1"
	TestReportContract         = "test-report/v1"
	CandidateRevisionContract  = "candidate-revision/v1"
	ImageDigestContract        = "image-digest/v1"
	ValidationResultContract   = "validation-result/v1"
	MergeRevisionContract      = "merge-revision/v1"
	MaxArtifactBytes           = 1 << 20
	MaxPatchBytes              = 256 << 10
	MaxCapturedTestOutputBytes = 1 << 20
	MaxTestOutputExcerptBytes  = 16 << 10
)

type ObjectIdentity struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type ChangeRequest struct {
	Summary            string   `json:"summary"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	RepositoryURL      string   `json:"repositoryURL"`
	SourceCommit       string   `json:"sourceCommit"`
}

type RepositoryRevision struct {
	RepositoryURL     string         `json:"repositoryURL"`
	RequestedRevision string         `json:"requestedRevision"`
	ResolvedCommit    string         `json:"resolvedCommit"`
	UtilityOperation  ObjectIdentity `json:"utilityOperation"`
}

type AffectedPath struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type AcceptanceMapping struct {
	CriterionIndex int    `json:"criterionIndex"`
	Verification   string `json:"verification"`
}

type ImplementationPlan struct {
	Summary                  string              `json:"summary"`
	ChangeRequestDigest      string              `json:"changeRequestDigest"`
	RepositoryRevisionDigest string              `json:"repositoryRevisionDigest"`
	SourceCommit             string              `json:"sourceCommit"`
	AffectedPaths            []AffectedPath      `json:"affectedPaths"`
	ImplementationSteps      []string            `json:"implementationSteps"`
	TestStrategy             []string            `json:"testStrategy"`
	AcceptanceMapping        []AcceptanceMapping `json:"acceptanceMapping"`
	Risks                    []string            `json:"risks"`
	Assumptions              []string            `json:"assumptions"`
}

type LineCounts struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

type ChangeSet struct {
	Format                   string     `json:"format"`
	Summary                  string     `json:"summary"`
	BaseCommit               string     `json:"baseCommit"`
	ChangeRequestDigest      string     `json:"changeRequestDigest"`
	ImplementationPlanDigest string     `json:"implementationPlanDigest"`
	Patch                    string     `json:"patch"`
	PatchDigest              string     `json:"patchDigest"`
	Files                    []string   `json:"files"`
	ByteCount                int        `json:"byteCount"`
	LineCounts               LineCounts `json:"lineCounts"`
}

type PreparedCandidate struct {
	RepositoryRevisionDigest string   `json:"repositoryRevisionDigest"`
	ChangeSetDigest          string   `json:"changeSetDigest"`
	BaseCommit               string   `json:"baseCommit"`
	Branch                   string   `json:"branch"`
	CandidateTree            string   `json:"candidateTree"`
	ChangedPaths             []string `json:"changedPaths"`
	PreparationCommandDigest string   `json:"preparationCommandDigest"`
}

type StreamEvidence struct {
	Digest        string `json:"digest"`
	CapturedBytes int    `json:"capturedBytes"`
	Excerpt       string `json:"excerpt"`
	Truncated     bool   `json:"truncated"`
}

type TestReport struct {
	PreparedCandidateDigest string         `json:"preparedCandidateDigest"`
	BaseCommit              string         `json:"baseCommit"`
	CandidateTree           string         `json:"candidateTree"`
	ObservedTreeAfter       string         `json:"observedTreeAfter"`
	WorkspaceClean          bool           `json:"workspaceClean"`
	CommandDigest           string         `json:"commandDigest"`
	EnvironmentImageDigest  string         `json:"environmentImageDigest"`
	Outcome                 string         `json:"outcome"`
	ExitCode                *int           `json:"exitCode,omitempty"`
	DurationMilliseconds    int64          `json:"durationMilliseconds"`
	Stdout                  StreamEvidence `json:"stdout"`
	Stderr                  StreamEvidence `json:"stderr"`
	TestCount               *int           `json:"testCount,omitempty"`
}

type CandidateRevision struct {
	RepositoryURL           string `json:"repositoryURL"`
	PreparedCandidateDigest string `json:"preparedCandidateDigest"`
	TestReportDigest        string `json:"testReportDigest"`
	ChangeSetDigest         string `json:"changeSetDigest"`
	SourceCommit            string `json:"sourceCommit"`
	Branch                  string `json:"branch"`
	Commit                  string `json:"commit"`
	Tree                    string `json:"tree"`
	CommitMessageDigest     string `json:"commitMessageDigest"`
}

type ImageDigest struct {
	CandidateRevisionDigest string `json:"candidateRevisionDigest"`
	ImageRepository         string `json:"imageRepository"`
	Digest                  string `json:"digest"`
	CandidateCommit         string `json:"candidateCommit"`
	CandidateTree           string `json:"candidateTree"`
	BuilderImageDigest      string `json:"builderImageDigest"`
	BuildCommandDigest      string `json:"buildCommandDigest"`
	DockerfileDigest        string `json:"dockerfileDigest"`
	ContextTree             string `json:"contextTree"`
}

type EndpointCheck struct {
	Name           string `json:"name"`
	Outcome        string `json:"outcome"`
	HTTPStatus     int    `json:"httpStatus"`
	ResponseDigest string `json:"responseDigest"`
}

type ReviewInstructions struct {
	Namespace       string `json:"namespace"`
	Service         string `json:"service"`
	LocalPort       int    `json:"localPort"`
	HealthPath      string `json:"healthPath"`
	CalculationPath string `json:"calculationPath"`
}

type ValidationResult struct {
	ValidationRun           ObjectIdentity     `json:"validationRun"`
	CandidateRevisionDigest string             `json:"candidateRevisionDigest"`
	ImageArtifactDigest     string             `json:"imageArtifactDigest"`
	CandidateCommit         string             `json:"candidateCommit"`
	CandidateTree           string             `json:"candidateTree"`
	ImageRepository         string             `json:"imageRepository"`
	ImageDigest             string             `json:"imageDigest"`
	InfrastructureRevision  string             `json:"infrastructureRevision"`
	Provider                string             `json:"provider"`
	Application             ObjectIdentity     `json:"application"`
	ObservedSourceRevision  string             `json:"observedSourceRevision"`
	ObservedImageDigest     string             `json:"observedImageDigest"`
	SyncStatus              string             `json:"syncStatus"`
	HealthStatus            string             `json:"healthStatus"`
	EndpointChecks          []EndpointCheck    `json:"endpointChecks"`
	ReviewInstructions      ReviewInstructions `json:"reviewInstructions"`
	StartedAt               string             `json:"startedAt"`
	CompletedAt             string             `json:"completedAt"`
	Outcome                 string             `json:"outcome"`
}

type RemoteProof struct {
	Ref            string `json:"ref"`
	ObservedCommit string `json:"observedCommit"`
	VerifiedAt     string `json:"verifiedAt"`
}

type MergeRevision struct {
	CandidateRevisionDigest string         `json:"candidateRevisionDigest"`
	ValidationResultDigest  string         `json:"validationResultDigest"`
	RepositoryURL           string         `json:"repositoryURL"`
	TargetBranch            string         `json:"targetBranch"`
	PreviousTargetCommit    string         `json:"previousTargetCommit"`
	CandidateCommit         string         `json:"candidateCommit"`
	CandidateTree           string         `json:"candidateTree"`
	MergeCommit             string         `json:"mergeCommit"`
	MergeTree               string         `json:"mergeTree"`
	ApprovalRequest         ObjectIdentity `json:"approvalRequest"`
	ApprovalDecision        ObjectIdentity `json:"approvalDecision"`
	ValidationRun           ObjectIdentity `json:"validationRun"`
	RemoteProof             RemoteProof    `json:"remoteProof"`
}
