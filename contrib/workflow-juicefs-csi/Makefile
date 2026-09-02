.PHONY: all fmt test build

all: test build

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

test:
	go test ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/workflow-juicefs-csi-driver ./cmd/workflow-juicefs-csi-driver
