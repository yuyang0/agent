#!/bin/sh

glide install

docker run --rm -v $GOPATH:/go -e GOPATH=/go -e GOBIN=/go/src/github.com/yuyang0/agent/bin golang:1.9.2 go install github.com/yuyang0/agent
