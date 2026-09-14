// Pure presentation adapter over the verified projection. No time/order-based
// joins, policy evaluation, or model-generated causal explanations occur here.
const lineageExplanations = (() => {
  function build(story, events, selectedEvent) {
    const nodes = new Map(story.nodes.map(node => [node.id, node]));
    const roles = [];
    const highlights = [];
    const decisionEvents = new Set([story.eventId]);
    const gap = message => ({ summary: message, roles: [], highlights: [], decisionEvents: [...decisionEvents], complete: false });
    const role = (label, value, eventId, ref, pins) => roles.push({ label, value, eventId, ref, pins });
    const mark = (ref, label) => { if (ref?.uid) highlights.push({ ref, label }); };
    const outgoing = (id, relation) => story.links.filter(link => link.from === id && link.relation === relation);

    if (story.kind === "recovery") {
      const decision = story.nodes.find(node => node.kind === "RecoveryDecision");
      for (const id of decision?.sourceEvents || []) decisionEvents.add(id);
      if (story.status !== "linked") return gap("The recovery explanation is incomplete. Inspect the recorded evidence gaps before drawing a causal conclusion.");
      const authorization = story.links.find(link => link.to === decision?.id && link.relation === "authorized_by" && nodes.get(link.from)?.kind === "StepAttempt");
      const replacement = nodes.get(authorization?.from);
      const triggerEdge = outgoing(replacement?.id, "triggered_by")[0];
      const priorEdge = outgoing(replacement?.id, "retry_of")[0];
      const trigger = nodes.get(triggerEdge?.to);
      const prior = nodes.get(priorEdge?.to);
      const selectionID = decision?.sourceEvents?.[0];
      const selection = events.get(selectionID)?.evidence;
      const retry = events.get(story.eventId)?.evidence;
      if (!replacement?.resource || !trigger?.resource || !prior?.resource || !selection?.restartStep || !retry?.attempt || retry.attempt.uid !== replacement.resource.uid || retry.decisionEvent !== selectionID) {
        return gap("The linked recovery needs its decision and replacement records to explain the restart. Those details are unavailable in this export.");
      }
      role("Trigger", "Failure that triggered this recovery", triggerEdge.eventId, trigger.resource);
      role("Authority / rule", selection.decision?.revision || "Recorded recovery selection", selectionID);
      role("Repeats", "Earlier attempt, recorded as " + (prior.outcome || "outcome unavailable"), priorEdge.eventId, prior.resource);
      role("Exact inputs", "Inputs pinned for the replacement attempt", story.eventId, null, retry.inputs || []);
      role("Consequence", "Replacement attempt recorded as created", story.eventId, replacement.resource);
      mark(trigger.resource, "Triggered this recovery");
      mark(prior.resource, "Attempt being repeated");
      mark(replacement.resource, "Created by this recovery");
      return {
        summary: "The recorded recovery decision selected “" + selection.restartStep + "” as the restart point in response to the linked failure. The replacement repeats a distinct earlier attempt; its creation does not establish that it later succeeded.",
        roles, highlights, decisionEvents: [...decisionEvents], complete: true
      };
    }

    if (story.kind === "approval") {
      const human = story.nodes.find(node => node.approval);
      const approval = human?.approval;
      const operations = story.links.filter(link => link.to === human?.id && link.relation === "approved_by" && nodes.get(link.from)?.kind === "UtilityOperation");
      for (const link of operations) {
        decisionEvents.add(link.eventId);
        for (const authorization of outgoing(link.from, "authorized_by")) {
          if (nodes.get(authorization.to)?.kind === "UtilityAuthorization") decisionEvents.add(authorization.eventId);
        }
      }
      if (story.status !== "linked") return gap("The approval-to-action explanation is incomplete. Admission may be recorded while downstream authorization is still pending, or evidence may be missing or inconsistent.");
      if (!approval?.approver?.subjectId || !approval.submissionEvent) return gap("The human identity or submission reference is unavailable for this explanation.");
      role("Human decision", approval.approver.subjectId + " submitted “" + approval.choice + "”", approval.submissionEvent, approval.stepAttempt);
      role("Authority / rule", "Decision admitted under the recorded approval policy", story.eventId);
      mark(approval.stepAttempt, "Human approval boundary");
      if (approval.choice === "Denied") {
        role("Reviewed inputs", "Exact inputs attached to the denied request", approval.inputEvent, null, approval.reviewedInputs);
        role("Consequence", "Denial admitted; this decision grants no approval to proceed", story.eventId);
        return { summary: approval.approver.subjectId + " denied the reviewed request. The admitted denial is not an execution authorization.", roles, highlights, decisionEvents: [...decisionEvents], complete: true };
      }
      const matching = operations.filter(link => link.eventId === selectedEvent || outgoing(link.from, "authorized_by").some(edge => edge.eventId === selectedEvent));
      const operationEdge = matching.length === 1 ? matching[0] : operations.length === 1 ? operations[0] : null;
      const operation = nodes.get(operationEdge?.from);
      const admission = events.get(operationEdge?.eventId)?.evidence;
      const binding = admission?.approvalBinding;
      const authorizationEdge = outgoing(operation?.id, "authorized_by").find(link => nodes.get(link.to)?.kind === "UtilityAuthorization");
      const authorization = events.get(authorizationEdge?.eventId)?.evidence;
      const inputs = events.get(authorization?.inputEvent)?.evidence;
      const pin = binding?.subject;
      const reviewed = approval.reviewedInputs?.find(input => input.name === pin?.name && input.artifactRef?.uid === pin?.artifactRef?.uid && input.digest === pin?.digest);
      const consumed = inputs?.inputs?.find(input => input.artifact?.uid === pin?.artifactRef?.uid && input.digest === pin?.digest && input.contract?.split("/")[0] === pin?.name);
      if (!operation || !authorization || !pin?.artifactRef?.uid || !pin.digest || !reviewed || !consumed) {
        return gap("The exact reviewed-to-authorized candidate comparison is unavailable here. Inspect the approval and authorization source records.");
      }
      role("Reviewed subject", "Exact subject in the human approval", approval.inputEvent, null, [reviewed]);
      role("Authorization input", "Same artifact UID and digest in the authorized execution", authorization.inputEvent, null, [pin]);
      role("Policy binding", "Requires “" + binding.requirement.step + "” for “" + binding.requirement.subject + "”", operationEdge.eventId);
      role("Consequence", "Operation authorized using this approval; authorization alone does not establish completion", authorizationEdge.eventId, operation.resource);
      const attemptEdge = outgoing(authorizationEdge.to, "authorized_by").find(link => nodes.get(link.to)?.kind === "StepAttempt");
      mark(nodes.get(attemptEdge?.to)?.resource || operation.resource, "Operation using this approval");
      // Show a runtime result only when it explicitly references this authorization.
      const completion = [...events.values()].find(event => event.type === "UtilityOperationCompleted" && event.evidence?.decisionEvent === authorizationEdge.eventId);
      if (completion) role("Reported result", completion.outcome || "Outcome not recorded", completion.eventId);
      return {
        summary: approval.approver.subjectId + " approved the exact reviewed “" + pin.name + "”. The recorded policy binding connects that admitted decision to authorization of “" + operation.resource.name + "”, with the same artifact UID and digest.",
        roles, highlights, decisionEvents: [...decisionEvents], complete: true
      };
    }
    return gap("No supported decision explanation is available for this relationship type.");
  }
  return { build };
})();

// Allows dependency-free unit tests with Node; the browser export uses the object above.
if (typeof module !== "undefined") module.exports = lineageExplanations;
