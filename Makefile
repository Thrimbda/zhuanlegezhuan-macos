GO ?= go
SWIFTC ?= swiftc

.PHONY: all test
all: bin/mirror bin/solve bin/solve-horizontal

bin:
	mkdir -p bin

bin/mirror: mirror.swift | bin
	$(SWIFTC) -parse-as-library -O $< -o $@

bin/solve: native/go.mod native/*.go native/cmd/zlgz-solver/*.go | bin
	cd native && $(GO) build -o ../$@ ./cmd/zlgz-solver

bin/solve-horizontal: native/go.mod native/*.go native/cmd/zlgz-solver/*.go | bin
	cd native && $(GO) build -ldflags '-X zlgz.shiftPolicy=horizontal' -o ../$@ ./cmd/zlgz-solver

test:
	cd native && $(GO) test ./...
