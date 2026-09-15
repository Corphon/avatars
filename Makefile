APP := avatars

.PHONY: build test test-race vet fmt run

build:
	go build -o bin/$(APP) ./cmd/avatars

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

run:
	go run ./cmd/avatars run "分析当前仓库并给出重构方案"