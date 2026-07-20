IMAGE ?= ghcr.io/loft-sh/kubevirt-provider-controller:dev
GOCACHE ?= /tmp/kubevirt-provider-controller-go-cache
GOMODCACHE ?= /tmp/kubevirt-provider-controller-go-mod

.PHONY: build test fmt image chart-lint chart-package

build:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build ./cmd/controller

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...

fmt:
	gofmt -w cmd internal

image:
	docker build -t $(IMAGE) .

chart-lint:
	cmp config/crd/bases/infra.loft.sh_kubevirtproviderclusters.yaml chart/crds/infra.loft.sh_kubevirtproviderclusters.yaml
	helm lint chart
	helm template test chart --namespace kubevirt-provider-controller-system > /dev/null

chart-package: chart-lint
	test -n "$(VERSION)"
	helm package chart --version "$(VERSION)" --app-version "$(VERSION)"
