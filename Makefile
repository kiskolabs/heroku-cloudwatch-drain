GOFMT_FILES?=$$(find . -name '*.go' |grep -v vendor)

deps:
	go mod tidy
	go mod vendor
build:
	GOARCH=amd64 GOOS=linux go build -o heroku-cloudwatch-drain 

.PHONY: deps build
