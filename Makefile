GOFMT_FILES?=$$(find . -name '*.go' |grep -v vendor)

deps:
	go mod tidy
	go mod vendor

.PHONY: deps
