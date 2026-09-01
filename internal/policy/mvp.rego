package sovereign

default decision := {"allowed": false, "reasons": ["policy denied the operation"], "evict": []}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "inference.share"
  input.request.model == input.endpoint.model
  input.request.modelRevision == input.endpoint.modelRevision
  input.request.tenant == input.endpoint.tenant
  input.request.classification == input.endpoint.classification
  input.request.sharingScope != "Dedicated"
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "inference.create"
  input.request.model != ""
  input.request.modelRevision != ""
  input.request.tenant != ""
  input.request.classification != ""
}

utility_non_privileged := {
  "repository.initialize",
  "git.createBranch",
  "candidate.prepare",
  "test.run",
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation in utility_non_privileged
  input.request.workflow != ""
  input.request.step != ""
  input.request.project != ""
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation == "git.commit"
  input.request.workflow != ""
  input.request.project != ""
  input.request.parameters.message != ""
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation == "git.push"
  input.request.credentialClass == "repository"
  input.request.hasCredential
  input.request.parameters.branch != ""
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation == "build.image"
  input.request.credentialClass == "registry"
  input.request.hasCredential
  input.request.parameters.imageName != ""
}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation == "git.merge"
  input.request.credentialClass == "repository"
  input.request.hasCredential
  input.request.parameters.sourceBranch != ""
  input.request.parameters.candidateRevision != ""
  input.request.parameters.approvalDecisionRef != ""
  input.request.parameters.validationRunRef != ""
}

# Request creation is separate from permission to merge. The runner verifies
# the accepted candidate, remote proof, and validation artifacts before POST.
decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.operation == "utility.execute"
  input.request.operation == "git.mergeRequest"
  input.request.credentialClass == "repository"
  input.request.hasCredential
}
