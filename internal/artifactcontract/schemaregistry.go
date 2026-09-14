package artifactcontract

import (
	"io/fs"

	"github.com/SovereignAI/internal/contractschema"
	artifactschemas "github.com/SovereignAI/schemas/artifacts/v1"
)

var contractSchemaFiles = map[string]string{
	ChangeRequestContract:        "change-request.schema.json",
	RepositoryRevisionContract:   "repository-revision.schema.json",
	BranchReferenceContract:      "branch-reference.schema.json",
	ImplementationPlanContract:   "implementation-plan.schema.json",
	TestChangeSetContract:        "test-change-set.schema.json",
	ChangeSetContract:            "change-set.schema.json",
	PreparedCandidateContract:    "prepared-candidate.schema.json",
	TestReportContract:           "test-report.schema.json",
	CandidateRevisionContract:    "candidate-revision.schema.json",
	CandidateRemoteProofContract: "candidate-remote-proof.schema.json",
	ImageDigestContract:          "image-digest.schema.json",
	ValidationResultContract:     "validation-result.schema.json",
	MergeRevisionContract:        "merge-revision.schema.json",
	MergeRequestContract:         "merge-request.schema.json",
}

type SchemaRegistry = contractschema.Registry
type SchemaDefinition = contractschema.Definition

func NewSchemaRegistry() (SchemaRegistry, error) {
	return LoadSchemaRegistry(artifactschemas.Files)
}

func LoadSchemaRegistry(schemaFS fs.FS) (SchemaRegistry, error) {
	return contractschema.Load(schemaFS, schemaFS, contractSchemaFiles, "")
}
