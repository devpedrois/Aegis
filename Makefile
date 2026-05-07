SHELL := cmd.exe
.SHELLFLAGS := /c
export GOCACHE := $(CURDIR)/.cache/go-build

.PHONY: build run test race lint clean

build:
	go build -ldflags="-s -w" -o bin/aegis ./cmd/aegis

run:
	go run ./cmd/aegis -c aegis.yml

test:
	go test -v ./...

race:
	go test -race -count=1 ./...

lint:
	go vet ./...

clean:
	if exist bin rmdir /s /q bin
