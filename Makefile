# tagalong — build & dev tasks
REPO ?= ghcr.io/timothydodd/tagalong

.PHONY: ui build run dev dev-ui test vet image clean

## ui: install deps and build the React SPA into ui/dist
ui:
	npm --prefix ui install
	npm --prefix ui run build

## build: build the UI then the Go binary (embeds ui/dist)
build: ui
	CGO_ENABLED=0 go build -ldflags="-s -w" -o tagalong ./cmd/tagalong

## run: run the backend against your kubeconfig (real deploys; cluster must be reachable)
run:
	TAGALONG_KUBECONFIG=$${KUBECONFIG:-$$HOME/.kube/config} \
	TAGALONG_DB_PATH=./dev.db \
	TAGALONG_LISTEN=:8080 \
	go run ./cmd/tagalong

## dev: run the backend with NO cluster (degraded mode) for UI/API development.
## Deploys return a clear "no cluster" error instead of hanging on a timeout.
dev:
	TAGALONG_DB_PATH=./dev.db TAGALONG_LISTEN=:8080 go run ./cmd/tagalong

## dev-ui: run the Vite dev server with hot reload (proxies /api + /hooks to :8080)
dev-ui:
	npm --prefix ui run dev

## test: run all Go tests
test:
	go test ./...

## vet: static checks
vet:
	go vet ./...

## image: build the multi-arch image with ko (no Docker) and push it to $(REPO).
## Needs `ko` (https://ko.build) and `ko login ghcr.io` first. CI does this on
## every push to main, so you rarely need it.
image: ui
	KO_DOCKER_REPO=$(REPO) ko build ./cmd/tagalong --bare \
		--platform=linux/amd64,linux/arm64 --tags latest

## clean: remove build artifacts
clean:
	rm -f tagalong dev.db dev.db-shm dev.db-wal
	rm -rf ui/dist/assets
