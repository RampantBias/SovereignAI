# Primary Roadblocks
- Agentic steps have consumed much of my recent time and are still not operational
  - architect and developer are still 50/50 for success; architect always misses at least 1 requirement in the request
  - test author has never succeeded, occasionally produces legible test but more often has tool call issues, repeat loops, etc.

Plan is to test out replacing my harness with an established codex harness. Most of what would be replaced is the reference agent
and associated internal plumbing. Agent wrapper would call the harness sdk instead. This doesn't necessarily hurt the decision
lineage thesis. On one hand, it supports the thesis by turning the agent into a pure black box. I never really wanted a harness in
the first place, but I slowly had to add the necessary infrastructure until it became a rudimentary harness. I'd rather focus on the infra
problems that still exist.

So next move would be testing codex harness under a few different scenarios to see if it helps the model generation. If that fails, I may 
have to cut my small model hypothesis, which again doesn't hurt my decision lineage but I want this to work with the smaller model, and
from the results I've seen I truly believe at least this task is possible. If the codex harness succeeds, I'll look to remove the
reference agent and add in an AgentHarness binary to operate the codex sdk.

# Secondary Roadblocks
- Adding in remaining audit records
  - candidate prep DecisionEvaluated
  - artifact accepted ConsequenceRecorded
  - validation evaluated DecisionEvaluated
  - recovery decision evaluated DecisionEvaluated
  - approval subject resolved InputsResolved
  - approval decision DecisionEvaluation
  - workflow completed consequence recorded
- Add visual display for decision lineage
  - Then evaluate what I'm still missing based on the operational questions I can ask
- add approval finalization to work in parallel with argo (and cause cleanup)
- Still having issues with validation run deployment, specifically build image and environment health/cleanup.
- fix flannel on wsl
- Verify merge request and build.image operate as intended

## Tertiary Roadblocks
- Documentation has fallen very far behind
  - Missing CRD/controller relationships
  - Missing state documents for each controller
  - Need more documentation on my decision lineage, wrap in my other notes on the research I've read
  - Demo notes and SovereignAI setup needs more testing and polishing, still very rough
- Lots of places ripe for refactor
  - MCP is a high priority along with some of the other controller codes