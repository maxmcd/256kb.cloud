./examples/tinygo/hello/hello.wasm: ./examples/tinygo/hello/main.go
	tinygo build -o $@ -target wasi $<

.PHONY: tidy
tidy: *.go
	go mod tidy

.PHONY: run
run: ./examples/tinygo/hello/hello.wasm tidy
	go run .

