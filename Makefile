
MAKEFLAGS += --jobs=8

./examples/tinygo/hello/hello.wasm: ./examples/tinygo/hello/main.go
	tinygo build -o $@ -target wasi $<

./examples/tinygo/counter/counter.wasm: ./examples/tinygo/counter/main.go
	tinygo build -o $@ -target wasi $<

.PHONY: tidy
tidy: *.go
	go mod tidy

build: tidy \
	./examples/tinygo/counter/counter.wasm \
	./examples/tinygo/hello/hello.wasm

.PHONY: run
run: build
	go run .

run_dev: build
	cd ./cmd/dev && go run .


