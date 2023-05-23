package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"golang.org/x/exp/slog"
)

var (
	memoryLimitPages = 4
)

//go:embed examples/tinygo/hello/hello.wasm
var helloWasm []byte

type Runtime struct{}

type Instance struct {
	runtime wazero.Runtime

	lock sync.Mutex

	connRecv      api.Function
	connections   []net.Conn
	networkBuffer []byte
}

func NewInstance(ctx context.Context, src []byte) (*Instance, error) {
	i := &Instance{}
	i.runtime = wazero.NewRuntimeWithConfig(
		ctx,
		wazero.NewRuntimeConfig().
			WithMemoryLimitPages(uint32(memoryLimitPages)),
	)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, i.runtime); err != nil {
		return nil, err
	}

	hostModuleBuilder := i.runtime.NewHostModuleBuilder("env")
	builder := hostModuleBuilder.NewFunctionBuilder()
	builder.WithFunc(i.connSend).Export("conn_send")
	if _, err := hostModuleBuilder.Instantiate(ctx); err != nil {
		return nil, err
	}

	mod, err := i.runtime.InstantiateWithConfig(ctx, helloWasm,
		wazero.NewModuleConfig().WithStdout(os.Stdout).WithStderr(os.Stderr))
	if err != nil {
		return nil, err
	}
	i.connRecv = mod.ExportedFunction("conn_recv")
	if i.connRecv == nil {
		return nil, fmt.Errorf("conn_recv function now found")
	}
	bufferPtrSize, err := mod.ExportedFunction("network_buffer").Call(ctx)
	if err != nil {
		return nil, err
	}
	bufferPtr := uint32(bufferPtrSize[0] >> 32)
	bufferSize := uint32(bufferPtrSize[0])
	i.networkBuffer, _ = mod.Memory().Read(bufferPtr, bufferSize)
	// b, _ := mod.Memory().Read(0, mod.Memory().Size())
	// os.Stdout.Write(b)
	slog.Info("Instance created")
	return i, nil
}

func (i *Instance) Close(ctx context.Context) error {
	return i.runtime.Close(ctx)
}

func (i *Instance) connSend(_ context.Context, m api.Module, connid, offset uint32) (errno uint32) {
	if int(connid) > len(i.connections) {
		// TODO
		return
	}
	conn := i.connections[connid-1]
	if conn == nil {
		// TODO
		return
	}
	if offset > uint32(len(i.networkBuffer))-1 {
		// TOOD
		return
	}
	_, err := conn.Write(i.networkBuffer[:offset])
	if err != nil {
		// TODO
		return
	}
	return
}

func (i *Instance) addConnection(conn net.Conn) int {
	i.lock.Lock()
	defer i.lock.Unlock()
	for idx, conn := range i.connections {
		if conn == nil {
			i.connections[idx] = conn
			return idx + 1
		}
	}
	i.connections = append(i.connections, conn)
	return len(i.connections)
}

func (i *Instance) Listen(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	slog.Info("Instance listening", "addr", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		idx := i.addConnection(conn)
		i.lock.Lock()
		if _, err := i.connRecv.Call(ctx, uint64(idx), 0); err != nil {
			fmt.Println("sockSend.Call err: ", err)
		}
		i.lock.Unlock()
		go func() {
			buf := make([]byte, len(i.networkBuffer))
			for {
				ln, err := conn.Read(buf)
				if err == io.EOF {
					return
				}
				i.lock.Lock()
				copy(i.networkBuffer[:ln], buf[:ln])
				if _, err := i.connRecv.Call(ctx, uint64(idx), uint64(ln)); err != nil {
					fmt.Println("sockSend.Call err: ", err)
				}
				i.lock.Unlock()
			}
		}()
	}
}

const html = `<!doctype html>
<html>
<head>
  <title>Example</title>
</head>
<body>
  256kb
  <script src="http://localhost:35729/livereload.js"></script>
</body>
</html>`

func main() {
	fmt.Println("Listening on port 3000")
	_ = http.ListenAndServe(":3000", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	i, err := NewInstance(context.Background(), helloWasm)
	if err != nil {
		log.Panicln(err)
	}
	if err := i.Listen(context.Background(), ":8080"); err != nil {
		log.Panicln(err)
	}
}
