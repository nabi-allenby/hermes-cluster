LM_DIR           := lifecycle-manager
IMAGE            := ghcr.io/nabi-allenby/hermes-cluster/lifecycle-manager
VERSION          ?= dev
MINIKUBE_PROFILE ?= minikube

.PHONY: help build test vet lint image image-load minikube-up \
        drill-substrate drill-agent drill-live \
        chart-lint e2e run-local clean

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

build: ## compile the lifecycle-manager
	cd $(LM_DIR) && go build ./...

test: ## unit tests
	cd $(LM_DIR) && go test ./...

vet:
	cd $(LM_DIR) && go vet ./...

lint: vet ## golangci-lint (falls back to go vet)
	@command -v golangci-lint >/dev/null 2>&1 && (cd $(LM_DIR) && golangci-lint run) || echo "golangci-lint not installed; ran go vet only"

image: ## build the lifecycle-manager image ($(IMAGE):$(VERSION))
	docker build -t $(IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) $(LM_DIR)

image-load: image ## build + load into minikube
	minikube -p $(MINIKUBE_PROFILE) image load $(IMAGE):$(VERSION)

minikube-up: ## start minikube + install pinned agent-sandbox
	hack/minikube-up.sh

drill-substrate: minikube-up ## claim -> suspend -> PVC survives -> resume
	hack/drills/0-substrate/run.sh

# Needs images hermes-agent + hrc in the local docker daemon and a seed home
# dir for make-seed.sh (see hack/README.md).
drill-agent: minikube-up ## real agent session: boot -> connect -> suspend -> wake -> drain
	hack/drills/1-agent-session/run.sh

# Needs a Discord bot token and PAC1_BOT_ID (see hack/README.md).
drill-live: minikube-up ## the chart fronting your real Discord bot
	hack/drills/2-live-discord/run.sh

chart-lint: ## helm lint + render check
	helm lint charts/hermes-cluster
	helm template t charts/hermes-cluster >/dev/null

# Expects a running minikube with agent-sandbox installed (make minikube-up).
e2e: ## e2e tiers 1-2 (minikube; +docker for the connector tier)
	cd $(LM_DIR) && go test -tags e2e -count=1 -timeout 20m ./e2e/...

# Run the lifecycle-manager locally against the minikube kubeconfig
# (kubectl apply -f hack/e2e/template.yaml provides the e2e-pool).
run-local: ## run the LM locally against your kubeconfig
	cd $(LM_DIR) && HLM_WARM_POOL=$${HLM_WARM_POOL:-e2e-pool} HLM_NAMESPACE=$${HLM_NAMESPACE:-default} \
		HLM_LOG_FORMAT=text go run ./cmd/lifecycle-manager

clean:
	rm -f $(LM_DIR)/lifecycle-manager
