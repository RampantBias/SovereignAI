package agentcontext

import (
	"fmt"
	"strings"
)

type Artifact struct {
	Name     string
	Contract string
	Digest   string
	Content  string
}

type Output struct {
	Contract  string
	MediaType string
	Guidance  []string
}

type Context struct {
	Role             string
	Responsibility   string
	RetryContext     string
	Capabilities     []string
	Output           *Output
	Artifacts        []Artifact
	DurableActions   []string
	FinalInstruction string
}

// Assemble builds the same prompt shape for every role and contract. Callers
// supply rendered artifacts and mechanics; artifact semantics remain data.
func Assemble(context Context) string {
	var output strings.Builder
	output.WriteString("# ROLE AND RESPONSIBILITY\nRole: ")
	output.WriteString(context.Role)
	output.WriteString("\nResponsibility: ")
	output.WriteString(context.Responsibility)
	writeSection(&output, context.RetryContext)

	output.WriteString("\n\n# CAPABILITIES\n")
	writeList(&output, context.Capabilities)
	if context.Output != nil {
		output.WriteString("\n# REQUIRED OUTPUT\nContract: ")
		output.WriteString(context.Output.Contract)
		output.WriteString("\nMedia Type: ")
		output.WriteString(context.Output.MediaType)
		output.WriteByte('\n')
		writeList(&output, context.Output.Guidance)
	}

	output.WriteString("\n# INPUT ARTIFACTS\n")
	for index, artifact := range context.Artifacts {
		output.WriteString(fmt.Sprintf("--- BEGIN INPUT ARTIFACT %d ---\n", index+1))
		output.WriteString("Name: ")
		output.WriteString(artifact.Name)
		output.WriteString("\nContract: ")
		output.WriteString(artifact.Contract)
		output.WriteString("\nDigest: ")
		output.WriteString(artifact.Digest)
		output.WriteString("\nRendered Content (bounded artifact data):\n")
		output.WriteString(artifact.Content)
		output.WriteString(fmt.Sprintf("\n--- END INPUT ARTIFACT %d ---\n", index+1))
	}

	output.WriteString("\n# DURABLE OUTPUT ACTIONS\n")
	writeList(&output, context.DurableActions)
	if strings.TrimSpace(context.FinalInstruction) != "" {
		output.WriteString("\n# FINAL INSTRUCTION\n")
		output.WriteString(strings.TrimSpace(context.FinalInstruction))
		output.WriteByte('\n')
	}
	return output.String()
}

func writeSection(output *strings.Builder, section string) {
	if strings.TrimSpace(section) == "" {
		return
	}
	output.WriteString("\n\n")
	output.WriteString(strings.TrimSpace(section))
	output.WriteByte('\n')
}

func writeList(output *strings.Builder, values []string) {
	for _, value := range values {
		output.WriteString("- ")
		output.WriteString(value)
		output.WriteByte('\n')
	}
}
