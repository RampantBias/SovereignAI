package artifactcontract

import (
	"io/fs"

	"github.com/SovereignAI/internal/contractschema"
	artifactschemas "github.com/SovereignAI/schemas/artifacts/v1"
)

var contractSchemaFiles = map[string]string{
	ChangeRequestContract:      "change-request.schema.json",
	RepositoryRevisionContract: "repository-revision.schema.json",
	ImplementationPlanContract: "implementation-plan.schema.json",
	TestChangeSetContract:      "test-change-set.schema.json",
	ChangeSetContract:          "change-set.schema.json",
	PreparedCandidateContract:  "prepared-candidate.schema.json",
	TestReportContract:         "test-report.schema.json",
	CandidateRevisionContract:  "candidate-revision.schema.json",
	ImageDigestContract:        "image-digest.schema.json",
	ValidationResultContract:   "validation-result.schema.json",
	MergeRevisionContract:      "merge-revision.schema.json",
}

type SchemaRegistry = contractschema.Registry
type SchemaDefinition = contractschema.Definition

func NewSchemaRegistry() (SchemaRegistry, error) {
	return LoadSchemaRegistry(artifactschemas.Files)
}

func LoadSchemaRegistry(schemaFS fs.FS) (SchemaRegistry, error) {
	return contractschema.Load(schemaFS, schemaFS, contractSchemaFiles, "")
}

// DerivePatchMetadata shares the canonical unified-diff parser with generation
// finalizers; validation remains authoritative for the resulting artifact.
func DerivePatchMetadata(patch string) ([]string, LineCounts, error) {
	files, added, deleted, err := inspectUnifiedDiff(patch)
	return files, LineCounts{Added: added, Deleted: deleted}, err
}
