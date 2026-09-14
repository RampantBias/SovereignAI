(() => {
  "use strict";

  // Go's html/template inserts an escaped string here, never executable evidence.
  const view = JSON.parse({{.}});
  const attempts = view.attempts || [];
  const stories = view.stories || [];
  const steps = view.steps || [];
  const byUID = new Map(attempts.map(attempt => [attempt.resource.uid, attempt]));
  const back = [];
  let selected = null;
  let checkpoint = null;
  let showRelationships = false;
  const eventIndex = new Map(attempts.flatMap(attempt => attempt.checkpoints).map(event => [event.eventId, event]));

  // Shared DOM helpers. All dynamic text is inserted with textContent.
  function el(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined && text !== null) node.textContent = String(text);
    if (className) node.className = className;
    return node;
  }

  function title(value) {
    return String(value || "").replace(/([a-z])([A-Z])/g, "$1 $2").replaceAll("_", " ");
  }

  function when(value) {
    return lineageTimestamps.format(value);
  }

  function outcomeClass(value) {
    const status = String(value || "").toLowerCase();
    if (["succeeded", "success", "passed", "allowed", "approved", "accepted", "authorized"].includes(status)) return "success";
    if (["failed", "failure", "interrupted", "denied", "rejected", "error"].includes(status)) return "failed";
    if (["pending", "awaitingapproval", "awaiting", "running", "preparing", "retrying"].includes(status)) return "pending";
    return "";
  }

  function outcome(value) {
    return el("span", title(value || "Not recorded"), "outcome " + outcomeClass(value));
  }

  function checkpointOutcome(event) {
    // A submitted choice is still only a submission. Admission can show its
    // recorded approved/denied choice without implying downstream execution.
    if (event.type === "ApprovalDecisionAdmitted" && event.evidence?.choice) return event.evidence.choice;
    return event.outcome;
  }

  function kv(root, label, value) {
    if (value === undefined || value === null || value === "") return;
    const row = el("div", null, "kv");
    const content = el("dd");
    content.append(value instanceof Node ? value : el("span", value));
    row.append(el("dt", label), content);
    root.append(row);
  }

  function group(root, label) {
    const section = el("section");
    section.append(el("h3", label));
    root.append(section);
    return section;
  }

  function button(label, action) {
    const control = el("button", label, "link");
    control.type = "button";
    control.addEventListener("click", action);
    return control;
  }

  function resource(ref) {
    if (!ref) return el("span", "Not recorded");
    const attempt = attempts.find(item => item.resource.uid === ref.uid || item.execution?.uid === ref.uid || item.approval?.resource.uid === ref.uid);
    return attempt ? button(ref.name + " →", () => select(attempt.resource.uid)) : el("span", ref.name || ref.uid || "Not recorded");
  }

  function source(id) {
    const current = byUID.get(selected);
    const attempt = current?.checkpoints.some(event => event.eventId === id) ? current : attempts.find(item => item.checkpoints.some(event => event.eventId === id));
    return attempt ? button(id, () => select(attempt.resource.uid, id)) : el("code", id || "Not recorded");
  }

  function deploymentLink(root, address) {
    try {
      const url = new URL(address);
      if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) return;
      const link = el("a", "Open recorded deployment");
      link.href = url.href;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      root.append(link);
    } catch {
      // An invalid recorded URL remains unavailable as a clickable link.
    }
  }

  // Evidence rendering: artifact pins, checks, decisions, and source references.
  function pins(root, values) {
    for (const pin of values || []) {
      if (!pin) continue;
      const details = el("dl", null, "evidence");
      kv(details, "Artifact", pin.contract || pin.name);
      kv(details, "Identity", resource(pin.artifact || pin.artifactRef));
      kv(details, "UID", pin.artifact?.uid || pin.artifactRef?.uid);
      kv(details, "Digest", el("code", pin.digest || "Not recorded"));
      if (pin.producer) kv(details, "Produced by", resource(pin.producer));
      root.append(details);
    }
  }

  function checks(root, values) {
    if (!values?.length) return;
    const list = el("ul");
    for (const check of values) {
      const item = el("li");
      item.append(el("span", check.id + " · "), outcome(check.outcome));
      if (check.reason) item.append(el("span", " — " + check.reason));
      if (check.expected || check.observed) {
        item.append(el("div", "Expected: " + (check.expected || "—") + " · Observed: " + (check.observed || "—"), "muted"));
      }
      list.append(item);
    }
    root.append(list);
  }

  function evidence(root, payload) {
    if (!payload) return;
    const details = el("dl");
    root.append(details);

    if (payload.approver) {
      kv(details, "Human decision", payload.choice);
      kv(details, "Submitted by", payload.approver.subjectId);
      kv(details, "Recorded groups", (payload.approver.groups || []).join(", "));
      kv(details, "Reason", payload.reason);
      kv(details, "Submitted at", when(payload.authoredAt));
      if (payload.admittedAt) kv(details, "Admitted at", when(payload.admittedAt));
      kv(details, "Request UID", payload.request?.uid);
      kv(details, "Decision UID", payload.decision?.uid);
      kv(details, "Required groups", (payload.approvalPolicy?.requiredGroups || []).join(", "));
      pins(root, payload.reviewedInputs);
    }
    if (payload.snapshotId) {
      kv(details, "Initial context", "Saved privately; contents are not embedded");
      kv(details, "Snapshot ID", el("code", payload.snapshotId));
      kv(details, "Digest", el("code", payload.digest));
      kv(details, "Saved at", when(payload.savedAt));
      kv(details, "Size", payload.byteCount + " bytes");
    }
    if (payload.consumer) {
      kv(details, "Input consumer", resource(payload.consumer));
      pins(root, payload.inputs);
    }
    if (payload.workspaceWrite) {
      kv(details, "Writer", payload.workspaceWrite.holderIdentity);
      kv(details, "Writer epoch", payload.workspaceWrite.writerEpoch);
      kv(details, "Capabilities", (payload.workspaceWrite.capabilities || []).join(", "));
    }
    if (payload.primitive) kv(details, "Execution subject", resource(payload.primitive));
    if (payload.decision?.kind) {
      kv(details, "Decision", payload.decision.kind + " · " + payload.decision.outcome);
      kv(details, "Rule revision", payload.decision.revision);
      kv(details, "Decision ID", el("code", payload.decision.id || ""));
      kv(details, "Decision input digest", el("code", payload.decision.inputDigest || ""));
    }
    if (payload.approvalBinding) {
      const binding = payload.approvalBinding;
      kv(details, "Required approval", binding.requirement?.step);
      kv(details, "Bound contract", binding.requirement?.subject);
      kv(details, "Request UID", binding.requestRef?.uid);
      kv(details, "Decision UID", binding.decisionRef?.uid);
      pins(root, [binding.subject]);
    }
    if (payload.retryOf) kv(details, "Repeats · retry_of", resource(payload.retryOf));
    if (payload.triggeredBy) kv(details, "Triggered by · triggered_by", resource(payload.triggeredBy));
    if (payload.attempt && payload.decisionEvent) kv(details, "Replacement attempt", resource(payload.attempt));
    if (payload.action) {
      kv(details, "Recovery action", payload.action);
      kv(details, "Restart step", payload.restartStep);
      kv(details, "Planned attempt", payload.nextAttemptName);
    }
    if (payload.feedback) kv(details, "Recorded failure feedback", payload.feedback.message || payload.feedback.code);

    for (const consequence of payload.consequences || []) {
      const result = el("dl", null, "evidence");
      kv(result, "Recorded consequence", consequence.kind);
      kv(result, "External result", consequence.externalId);
      kv(result, "Target", consequence.target);
      kv(result, "Before", consequence.before);
      kv(result, "After", consequence.after);
      if (consequence.artifactEvidence) pins(result, [consequence.artifactEvidence]);
      root.append(result);
    }
    checks(root, payload.invariants);

    const references = [payload.authorityEvent, payload.inputEvent, payload.submissionEvent, payload.decisionEvent, payload.approvalBinding?.admissionEventId, ...(payload.evidenceEvents || [])].filter(Boolean);
    if (references.length) {
      const panel = el("details");
      panel.append(el("summary", "Source event references"));
      for (const id of references) {
        const row = el("p");
        row.append(source(id));
        panel.append(row);
      }
      root.append(panel);
    }
  }

  // Keep resource types visible: an execution, its attempt, and its decision
  // often share a name. Navigation controls must not look like extra graph edges.
  function relationshipEndpoint(node, id, authority = false) {
    const labels = {
      AgentRun: "Agent execution", AgentAuthorization: "Agent authorization decision",
      UtilityOperation: "Utility operation", UtilityAuthorization: "Utility authorization decision",
      StepAttempt: "Step attempt", SovereignWorkflow: "Workflow",
      RecoveryDecision: "Recovery decision", ApprovalDecision: "Human approval decision",
      ApprovalRequest: "Approval request", HumanSubmission: "Human submission",
      SavedContext: "Saved context", TestFailure: "Test failure",
    };
    const kind = node?.kind || node?.resource?.kind;
    let label = labels[kind] || title(kind || "Unknown node");
    if (authority) label = "Step attempt authority";
    if (kind === "Artifact" && node.label !== node.resource?.name) label += " · " + node.label;
    const name = node?.resource?.name || node?.label || id;
    const endpoint = el("div", null, "relationship-endpoint");
    const ref = node?.resource;
    const target = ref && attempts.find(item => item.resource.uid === ref.uid || item.execution?.uid === ref.uid || item.approval?.resource.uid === ref.uid);
    const eventID = node?.id?.startsWith("event:") ? node.id.slice(6) : node?.sourceEvents?.[0];
    const heading = target ? button(label, () => select(target.resource.uid)) : !ref && eventID ? source(eventID) : el("span", label);
    heading.textContent = label;
    heading.classList.add("relationship-kind");
    if (heading.tagName === "BUTTON") heading.setAttribute("aria-label", "Inspect " + label + ": " + name);
    endpoint.append(heading, el("span", name, "relationship-name muted"));
    return endpoint;
  }

  function relationships(root, attempt) {
    const relatedStories = stories.filter(item => attempt.storyIds.includes(item.eventId));
    if (!relatedStories.length) {
      root.append(el("p", "No recovery or approval relationship story is associated with this attempt in this export.", "muted"));
      root.append(el("p", "This view currently projects those two kinds of decisions. Inspect recorded events such as InputsResolved for the attempt’s input evidence.", "muted"));
      if (!stories.length && view.phase === "AwaitingApproval") {
        root.append(el("p", "This snapshot was exported while the workflow awaited approval. Export again after the decision is admitted to inspect the recorded approval relationships.", "muted"));
      }
      return;
    }
    for (const story of relatedStories) {
      const section = group(root, title(story.kind) + " relationships");
      section.append(el("p", "Evidence: " + story.status, story.status === "linked" ? "muted" : "issue"));
      for (const issue of story.issues || []) section.append(el("p", issue.reason, "issue"));

      const nodes = new Map(story.nodes.map(node => [node.id, node]));
      const mine = new Set(story.nodes.filter(node => node.resource && [attempt.resource.uid, attempt.execution?.uid, attempt.approval?.resource.uid].includes(node.resource.uid)).map(node => node.id));
      function draw(parent, links) {
        for (const link of links) {
          const from = nodes.get(link.from);
          const to = nodes.get(link.to);
          const executionFor = link.relation === "produced_by" && from?.kind === "AgentRun" && to?.kind === "StepAttempt";
          const groundedIn = link.relation === "authorized_by" && ["AgentAuthorization", "UtilityAuthorization"].includes(from?.kind) && to?.kind === "StepAttempt";
          const label = executionFor ? "execution for" : groundedIn ? "grounded in" : link.relation.replaceAll("_", " ");
          const record = el("div", null, "recorded-relationship");
          record.dataset.relation = link.relation;
          record.dataset.fromKind = from?.kind || "";
          record.dataset.toKind = to?.kind || "";
          const row = el("div", null, "relationship-row");
          const relation = el("span", label + " ", "relationship-label");
          const arrow = el("span", "→", "relationship-arrow");
          arrow.setAttribute("aria-hidden", "true");
          relation.append(arrow);
          row.append(
            relationshipEndpoint(from, link.from), relation,
            relationshipEndpoint(to, link.to, groundedIn)
          );
          const recorded = el("details", null, "relationship-source");
          recorded.append(el("summary", "Recorded relationship"));
          kv(recorded, "Relation", el("code", link.relation));
          kv(recorded, "From node", el("code", link.from));
          kv(recorded, "To node", el("code", link.to));
          kv(recorded, "Source event", source(link.eventId));
          record.append(row, recorded);
          parent.append(record);
        }
      }
      draw(section, story.links.filter(link => mine.has(link.from) || mine.has(link.to)));
      const more = el("details");
      more.append(el("summary", "Inspect the full evidence bridge"));
      draw(more, story.links);
      for (const node of story.nodes) {
        const details = el("details");
        details.append(el("summary", node.label + " · " + node.kind + (node.outcome ? " · " + node.outcome : "")));
        if (node.summary) details.append(el("p", node.summary));
        if (node.artifact) pins(details, [node.artifact]);
        evidence(details, node.approval || node.context);
        if (node.accessURL) deploymentLink(details, node.accessURL);
        for (const id of node.sourceEvents || []) {
          const row = el("p");
          row.append(source(id));
          details.append(row);
        }
        more.append(details);
      }
      section.append(more);
    }
  }

  // Revealed interpretation stays separate from the inspectable event payloads.
  function highlightHistory(highlights) {
    for (const row of document.querySelectorAll("[data-attempt]")) {
      const attempt = byUID.get(row.dataset.attempt);
      const labels = highlights.filter(item => [attempt.resource.uid, attempt.execution?.uid, attempt.approval?.resource.uid].includes(item.ref.uid)).map(item => item.label);
      row.classList.toggle("related", labels.length > 0);
      row.querySelector(".relation-label")?.remove();
      if (labels.length) row.append(el("span", [...new Set(labels)].join(" · "), "relation-label"));
    }
  }

  function decisionPanel(root, attempt) {
    const related = stories.filter(story => attempt.storyIds.includes(story.eventId));
    const models = related.map(story => ({ story, model: lineageExplanations.build(story, eventIndex, checkpoint) }));
    const active = models.filter(item => item.model.decisionEvents.includes(checkpoint));
    const panel = group(root, active.length === 1 ? "Decision explanation" : "Related decisions");
    panel.classList.add("decision-explanation");
    panel.id = "decision-explanation";
    if (active.length !== 1) {
      panel.append(el("p", models.length ? "This event is evidence. Inspect a linked decision to see the role it played." : "No supported recovery or approval decision is linked to this attempt in this export.", "muted"));
      for (const { story, model } of models) {
        // Prefer the recovery selection itself, rather than its creation marker.
        const id = story.kind === "recovery" ? model.decisionEvents.find(id => id !== story.eventId && eventIndex.has(id)) || story.eventId : story.eventId;
        const row = el("p");
        const target = eventIndex.has(id) ? button("Inspect " + story.kind + " decision", () => {
          const current = attempt.checkpoints.some(event => event.eventId === id) ? attempt : attempts.find(item => item.checkpoints.some(event => event.eventId === id));
          if (current) select(current.resource.uid, id);
        }) : el("span", "Decision source unavailable: " + id);
        row.append(target);
        panel.append(row);
      }
      return;
    }
    const { story, model } = active[0];
    panel.append(el("p", model.summary, model.complete ? "" : "issue"));
    for (const issue of story.issues || []) panel.append(el("p", issue.reason, "issue"));
    for (const role of model.roles) {
      const details = el("dl");
      const content = el("div");
      content.append(el("span", role.value));
      if (role.ref) {
        const link = el("div");
        link.append(resource(role.ref));
        content.append(link);
      }
      if (role.pins) {
        const pinDetails = el("details");
        pinDetails.append(el("summary", role.pins.length ? "Inspect exact pins: " + role.pins.map(pin => pin.name || pin.contract).join(", ") : "No input pins recorded"));
        pins(pinDetails, role.pins);
        content.append(pinDetails);
      }
      const reference = el("div", null, "role-source muted");
      const sourceLink = source(role.eventId);
      if (sourceLink.tagName === "BUTTON") {
        sourceLink.textContent = title(eventIndex.get(role.eventId)?.type) + " · " + role.eventId.slice(0, 10);
        sourceLink.setAttribute("aria-label", "Inspect source event " + role.eventId);
      }
      reference.append(el("span", "Source: "), sourceLink);
      content.append(reference);
      kv(details, role.label, content);
      panel.append(details);
    }
    highlightHistory(model.highlights);
  }

  function clearSelection(updateURL = true) {
    selected = null;
    checkpoint = null;
    back.length = 0;
    if (updateURL) history.replaceState(null, "", location.pathname + location.search);
    document.getElementById("detail").replaceChildren();
    document.getElementById("checkpoints").replaceChildren();
    document.getElementById("checkpoint-context").textContent = "";
    document.getElementById("event-heading").hidden = true;
    highlightHistory([]);
    updateHistorySelection();
  }

  // Navigation: preserve the lists while changing details, so their scroll and
  // keyboard focus do not reset every time a checkpoint is selected.
  function select(uid, eventID, remember = true) {
    if (!byUID.has(uid)) return;
    if (remember && selected) back.push([selected, checkpoint]);
    const changedAttempt = selected !== uid;
    selected = uid;
    const attempt = byUID.get(uid);
    checkpoint = eventID || (attempt.checkpoints.find(event => ["StepAttemptFailed", "StepAttemptInterrupted"].includes(event.type)) || attempt.checkpoints.find(event => event.type === "ApprovalDecisionAdmitted") || attempt.checkpoints.at(-1))?.eventId;
    history.replaceState(null, "", "#" + encodeURIComponent(uid));
    updateHistorySelection();
    renderCheckpoints(attempt);
    renderDetail(attempt);
    if (changedAttempt) document.getElementById("checkpoint-pane").scrollTop = 0;
    document.getElementById("detail").scrollTop = 0;
    document.querySelector(".checkpoint[aria-pressed='true']")?.scrollIntoView({ block: "nearest" });
  }

  function renderHistory() {
    const root = document.getElementById("history");
    root.replaceChildren();
    let iteration = null;
    for (const attempt of attempts) {
      if (iteration !== attempt.iteration) {
        iteration = attempt.iteration;
        root.append(el("div", "Workflow iteration " + iteration, "iteration"));
      }
      const row = el("button", null, "selection-row attempt");
      row.type = "button";
      row.dataset.attempt = attempt.resource.uid;
      const step = steps.findIndex(item => item.name === attempt.step);
      const heading = el("span", null, "row-heading");
      heading.append(el("span", (step < 0 ? "" : String(step + 1).padStart(2, "0") + " · ") + attempt.step, "row-name"), outcome(attempt.status.phase || "Pending"));
      row.append(heading, el("span", attempt.kind + " · Workflow " + attempt.iteration + " · Retry " + attempt.retry, "row-meta"));
      row.addEventListener("click", () => selected === attempt.resource.uid ? clearSelection() : select(attempt.resource.uid));
      root.append(row);
    }
    const remaining = steps.filter(step => !attempts.some(attempt => attempt.step === step.name));
    const unstarted = document.getElementById("unstarted");
    if (remaining.length) {
      unstarted.append(el("h3", "No attempt observed"));
      for (const step of remaining) unstarted.append(el("p", step.name, "muted"));
    }
  }

  function updateHistorySelection() {
    for (const row of document.querySelectorAll("[data-attempt]")) row.setAttribute("aria-pressed", String(row.dataset.attempt === selected));
  }

  function renderCheckpoints(attempt) {
    const root = document.getElementById("checkpoints");
    root.replaceChildren();
    document.getElementById("event-heading").hidden = false;
    document.getElementById("checkpoint-context").textContent = attempt.step + " · " + attempt.checkpoints.length + " recorded";
    if (!attempt.checkpoints.length) root.append(el("p", "No associated audit events are available.", "muted"));
    const tiedEvents = lineageTimestamps.tiedEventIDs(attempt.checkpoints);
    if (tiedEvents.size) root.append(el("p", "Some events share a timestamp. Their order is not established by time; inspect the recorded relationships.", "muted"));
    for (const event of attempt.checkpoints) {
      const row = el("button", null, "selection-row checkpoint");
      row.type = "button";
      row.dataset.checkpoint = event.eventId;
      row.setAttribute("aria-pressed", String(event.eventId === checkpoint));
      const heading = el("span", null, "row-heading");
      heading.append(el("span", title(event.type), "row-name"), outcome(checkpointOutcome(event)));
      const timestamp = el("span", when(event.occurredAt) + (tiedEvents.has(event.eventId) ? " · Same timestamp" : ""), "row-meta");
      timestamp.title = event.occurredAt || "Not recorded";
      row.append(heading, timestamp);
      row.addEventListener("click", () => {
        checkpoint = event.eventId;
        for (const other of root.querySelectorAll("[data-checkpoint]")) other.setAttribute("aria-pressed", String(other === row));
        renderDetail(attempt);
        document.getElementById("detail").scrollTop = 0;
      });
      root.append(row);
    }
  }

  // Right pane: keep live failure context visible alongside the selected event.
  function renderDetail(attempt) {
    const root = document.getElementById("detail");
    root.replaceChildren();
    highlightHistory([]);
    if (back.length) {
      const control = button("← Return to previous selection", () => {
        const [uid, event] = back.pop();
        select(uid, event, false);
      });
      control.id = "back";
      root.append(control);
    }
    root.append(el("h2", attempt.step), el("div", attempt.kind + " · Workflow " + attempt.iteration + " · Retry " + attempt.retry, "muted"));
    if (showRelationships) decisionPanel(root, attempt);
    const identity = el("details");
    identity.append(el("summary", "Attempt identity"), el("code", attempt.resource.name + "\nUID: " + attempt.resource.uid + "\nCreated: " + when(attempt.createdAt)));
    root.append(identity);
    const status = el("div", null, "status");
    status.append(el("span", "Observed status: "), outcome(attempt.status.phase || "Pending"), el("span", " · live StepAttempt", "muted"));
    root.append(status);

    if (attempt.status.failureReason) {
      const failure = group(root, "Failure");
      failure.append(el("p", attempt.status.failureReason, "failed"));
      if (attempt.status.failureMessage) failure.append(el("p", attempt.status.failureMessage));
      failure.append(el("p", "Execution marked retryable: " + String(attempt.status.retryable || false) + ". This does not establish that a retry was selected.", "muted"));
    }
    for (const issue of attempt.issues || []) root.append(el("p", issue, "issue"));

    const event = attempt.checkpoints.find(item => item.eventId === checkpoint);
    const section = group(root, event ? title(event.type) : "Recorded evidence");
    if (event) {
      section.id = "selected-checkpoint";
      section.append(outcome(checkpointOutcome(event)));
      if (event.reason) section.append(el("p", event.reason));
      evidence(section, event.evidence);
      if (event.accessURL) deploymentLink(section, event.accessURL);
      for (const [relation, name] of Object.entries(event.relatedAttempts || {})) {
        if (!name) continue;
        const target = attempts.find(item => item.resource.name === name);
        const row = el("p", title(relation) + ": ");
        row.append(target ? resource(target.resource) : el("span", name + " (no attempt observed)"));
        section.append(row);
      }
      const refs = el("details");
      refs.append(el("summary", "Event source"), el("code", event.eventId), el("p", when(event.occurredAt) + " · Bound by " + event.binding, "muted"));
      refs.append(el("p", "Stored timestamp: " + (event.occurredAt || "Not recorded"), "muted"));
      if (event.type === "StepAttemptRetried" && event.evidence?.decisionEvent) {
        refs.append(el("p", (event.evidence.observedAt ? "Timestamp basis: replacement observed by the controller." : "Timestamp basis: legacy Kubernetes creation time (or recovery selection fallback)."), "muted"));
      }
      section.append(refs);
    } else {
      section.append(el("p", "Live status alone is not evidence of authorization."));
    }

    const config = steps.find(step => step.name === attempt.step);
    if (config?.requiresApproval) {
      const boundary = group(root, "Declared approval boundary");
      boundary.append(el("p", "Requires " + config.requiresApproval.step + " for " + config.requiresApproval.subject), el("p", "Workflow declaration. Recorded admission and authorization establish whether this approval was used.", "muted"));
    }
    if (attempt.approval) {
      const approval = group(root, "Approval request · live resource");
      const details = el("dl");
      approval.append(details);
      kv(details, "Request phase", attempt.approval.phase);
      kv(details, "Request UID", attempt.approval.resource.uid);
      kv(details, "Required groups", (attempt.approval.policy.requiredGroups || []).join(", "));
      kv(details, "Mode", attempt.approval.policy.mode);
      pins(approval, attempt.approval.inputs);
      approval.append(el("p", "Submission, admission, and downstream authorization are separate audit events.", "muted"));
    }
    if (showRelationships) {
      const bridge = el("details");
      bridge.append(el("summary", "Inspect recorded relationships"));
      relationships(bridge, attempt);
      root.append(bridge);
    }
    if (showRelationships && ["Failed", "Interrupted"].includes(attempt.status.phase) && !attempt.storyIds.length) {
      root.append(el("p", "No supported recovery story is linked. Inspect the recorded events; absence of a story does not prove recovery was forbidden.", "muted"));
    }
  }

  // Initial snapshot and selection restored from the URL on reload.
  document.getElementById("title").textContent = view.workflow.name;
  document.getElementById("stamp").textContent = "Snapshot " + when(view.exportedAt) + " · " + view.workflow.namespace + " · " + view.workflow.uid;
  document.getElementById("route").textContent = (attempts.length ? "Started at " + attempts[0].step : "No attempt observed") + " → Workflow status: " + (view.phase || "Pending");
  for (const issue of view.issues || []) document.getElementById("route").append(el("p", issue, "issue"));
  renderHistory();
  document.getElementById("reveal").checked = false;
  document.getElementById("reveal").addEventListener("change", event => {
    showRelationships = event.target.checked;
    const attempt = byUID.get(selected);
    if (attempt) renderDetail(attempt);
    document.getElementById("detail").scrollTop = 0;
  });
  let initial;
  try { initial = decodeURIComponent(location.hash.slice(1)); } catch { /* Ignore malformed fragments. */ }
  if (byUID.has(initial)) select(initial, null, false);
  else clearSelection();
  window.addEventListener("hashchange", () => {
    try {
      const uid = decodeURIComponent(location.hash.slice(1));
      if (byUID.has(uid)) select(uid, null, false);
      else clearSelection(false);
    } catch { clearSelection(false); }
  });
})();
