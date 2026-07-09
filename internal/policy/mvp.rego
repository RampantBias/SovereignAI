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
