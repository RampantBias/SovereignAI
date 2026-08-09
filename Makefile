.PHONY: generate manifests test test-integration images verify

generate:
	controller-gen object paths=./internal/api/v1alpha1/...

manifests:
	controller-gen crd rbac:roleName=sovereign-manager-role paths=./internal/api/v1alpha1/... output:crd:artifacts:config=config/crd/bases

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

images:
	docker build --target controller -t sovereign-controller:dev .
	docker build --target api -t sovereign-api:dev .
	docker build --target cli -t sovereign-cli:dev .
	docker build --target agent-wrapper -t sovereign-agent-wrapper:dev .
	docker build --target reference-agent -t sovereign-reference-agent:dev .
	docker build --target utility-runner -t sovereign-utility-runner:dev .
	docker build --target smoke-agent -t sovereign-smoke-agent:dev .
	docker build --target mcp-server -t sovereign-mcp-server:dev .
	docker build --target artifact-collector -t sovereign-artifact-collector:dev .
	docker build --target artifact-bootstrap -t sovereign-artifact-bootstrap:dev .

verify: test
	git diff --exit-code
