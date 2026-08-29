package controllers

func ExecutionControlPath(name string) string { return "/workspace/attempts/" + name + "/control" }
func ExecutionStagingPath(name string) string { return "/workspace/attempts/" + name + "/staging" }
func ExecutionResultPath(name string) string  { return ExecutionControlPath(name) + "/result.json" }
func ExecutionAuditEventsPath(name string) string {
	return ExecutionControlPath(name) + "/events.jsonl"
}
