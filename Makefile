LM_DIR        := lifecycle-manager
IMAGE         := ghcr.io/nabi-allenby/hermes-cluster/lifecycle-manager
VERSION       ?= dev
MINIKUBE_PROFILE ?= minikube

.PHONY: build test lint vet image image-load minikube-up p-m0 e2e run-local clean

build:
	cd $(LM_DIR) && go build ./...

test:
	cd $(LM_DIR) && go test ./...

vet:
	cd $(LM_DIR) && go vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && (cd $(LM_DIR) && golangci-lint run) || echo "golangci-lint not installed; ran go vet only"

image:
	docker build -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) $(LM_DIR)

image-load: image
	minikube -p $(MINIKUBE_PROFILE) image load $(IMAGE):$(VERSION)

minikube-up:
	hack/minikube-up.sh

p-m0: minikube-up
	hack/p-m0/run.sh

# e2e expects a running minikube with agent-sandbox installed (make minikube-up).
e2e:
	cd $(LM_DIR) && go test -tags e2e -count=1 -timeout 20m ./e2e/...

# Run the lifecycle-manager locally against the minikube kubeconfig.
run-local:
	cd $(LM_DIR) && HLM_WARM_POOL=$${HLM_WARM_POOL:-hello-world} HLM_NAMESPACE=$${HLM_NAMESPACE:-default} \
		HLM_LOG_FORMAT=text go run ./cmd/lifecycle-manager

clean:
	rm -f $(LM_DIR)/lifecycle-manager
