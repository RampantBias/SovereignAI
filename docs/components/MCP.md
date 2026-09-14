# MCP

MCP was initially integrated on 8/27/26 (just a week before my MVP demo is due!). I had at one time dropped adding MCP, leaving it in an early 'exploration' mode as a deployment so I could understand it.
At the time it felt like I could operate the agent without it, relying on receiving JSON contracts and diffs through request reponses. However, I found that for my MVP
while the Architect worked fine with its contract the test author and developer had consistent issues, often producing invalid diffs or just squeezing a valid format by only for me to later find
the artifacts were not compilable/operable and often didn't even contain the solution code.

My solution was to integrate MCP which I will document here

## Operation
MCP containers are mounted as sidecars per each agent run. The MCP client uses MCP Go SDK.

## Capabilities
Capabilities represent the tools available to an agent. I've provided 6 tools relevant to my MVP demo.

- workspace_tree
  - 
- workspace_read
  - 
- workspace_search
  - 
- workspace_write
  - 
- workspace_create
  - gives agent ability to create a new file in the repository workspace
- workspace_replace
  - 
- workspace_delete
  - 
- candidate_write
  - publishes agent's output artifact directly to the associated attempt directory; does not write to the repository workspace
- agent_complete
  - Allows the model to report a completion status rather than engage in looping behavior.

CapabilityWorkspaceRead    = "workspace_read"
	CapabilityWorkspaceWrite   = "workspace_write"
	CapabilityWorkspaceCreate  = "workspace_create"
	CapabilityWorkspaceSearch  = "workspace_search"
	CapabilityWorkspaceReplace = "workspace_replace"
	CapabilityWorkspaceDelete  = "workspace_delete"
	CapabilityWorkspaceTree    = "workspace_tree"
	CapabilityCandidateWrite   = "candidate_write"
	CapabilityAgentComplete    = "agent_complete"