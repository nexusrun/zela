.PHONY: build test test-ha test-gateway test-cluster-live test-docker-live check linux workers cli demo run
build: workers
	mkdir -p bin
	go build -trimpath -o bin/zela-server ./cmd/zela-server
	go build -trimpath -o bin/zela-worker ./cmd/worker
	go build -trimpath -o bin/zela ./cmd/zela

test:
	go test -race ./...
	node --test scripts/frontend.test.cjs

test-ha:
	go test -race ./internal/control -run 'TestHA|TestReadinessWithoutHA' -count=1 -v

test-gateway:
	go test -race ./internal/control ./internal/workerproxy ./internal/dockerapi -run 'TestDockerGateway|TestInfrastructure|TestIntegrationToken|TestDeploymentToken|TestWorker|TestEnginePaths' -count=1 -v

test-docker-live:
	@test -n "$$ZELA_TEST_DOCKER_SOCKET" || { printf '%s\n' 'Set ZELA_TEST_DOCKER_SOCKET to an accessible Docker Unix socket.' >&2; exit 1; }
	@test -n "$$ZELA_TEST_DOCKER_IMAGE" || { printf '%s\n' 'Set ZELA_TEST_DOCKER_IMAGE to an existing Linux image with sh and sleep.' >&2; exit 1; }
	go test -race ./internal/control -run '^TestDockerGatewayLiveDocker$$' -count=1 -v

test-cluster-live:
	@test -n "$$ZELA_TEST_CLUSTER_URL" || { printf '%s\n' 'Set ZELA_TEST_CLUSTER_URL to the running control-plane HTTP(S) origin.' >&2; exit 1; }
	@test -n "$$ZELA_TEST_ADMIN_TOKEN_FILE" || { printf '%s\n' 'Set ZELA_TEST_ADMIN_TOKEN_FILE to the control-plane token file.' >&2; exit 1; }
	@test -n "$$ZELA_TEST_DOCKER_IMAGE" || { printf '%s\n' 'Set ZELA_TEST_DOCKER_IMAGE to an image already on that worker with sh and sleep.' >&2; exit 1; }
	go test -race ./internal/control -run '^TestDockerGatewayLiveEndpoint$$' -count=1 -v

check:
	go vet ./...
	node --check internal/control/web/app.js
	node --check internal/control/web/monitoring.js
	node --check internal/control/web/notifications.js

workers:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/zela-worker-linux-amd64 ./cmd/worker
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/zela-worker-linux-arm64 ./cmd/worker

cli:
	mkdir -p bin
	go build -trimpath -o bin/zela ./cmd/zela

linux: workers
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/zela-server-linux-amd64 ./cmd/zela-server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/zela-server-linux-arm64 ./cmd/zela-server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/zela-linux-amd64 ./cmd/zela
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/zela-linux-arm64 ./cmd/zela

run:
	go run ./cmd/zela-server

demo:
	go run ./cmd/zela-server --demo
