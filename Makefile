
MAKEFLAGS += --jobs=8

./examples/tinygo/chat/main.wasm: ./examples/tinygo/chat/main.go
	tinygo build -o $@ -target wasi $<

./examples/tinygo/counter/main.wasm: ./examples/tinygo/counter/main.go
	tinygo build -o $@ -target wasi $<


# ./sql/models.go: ./sql/*.sql
# 	cd sql && go run github.com/kyleconroy/sqlc/cmd/sqlc@v1.18.0 generate

.PHONY: tidy
tidy: *.go
	go mod tidy

build: tidy \
	./examples/tinygo/counter/main.wasm \
	./examples/tinygo/chat/main.wasm

deploy_linux:
	env GOOS=linux GOARCH=amd64 go build .
	rsync -avz 256kb.cloud  root@5.161.53.66:~/app/256kb.cloud
	rsync -avz 256kb.service  root@5.161.53.66:/usr/lib/systemd/system/256kb.service
	ssh root@5.161.53.66 "systemctl daemon-reload && service 256kb restart && service 256kb status"

tail_logs:
	ssh root@5.161.53.66 "journalctl -f -u 256kb"

.PHONY: run
run: build
	go run .

run_dev: build
	cd ./cmd/dev && go run .


