# tagalong — build & dev tasks
IMAGE ?= timdoddcool/tagalong:latest

.PHONY: ui build run dev dev-ui test vet docker clean

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

## docker: build the container image
docker:
	docker build -t $(IMAGE) .

## clean: remove build artifacts
clean:
	rm -f tagalong dev.db dev.db-shm dev.db-wal
	rm -rf ui/dist/assets
