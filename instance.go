package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"golang.org/x/exp/slog"
	"golang.org/x/sync/semaphore"
)

var (
	memoryLimitPages = 4
	//go:embed examples/tinygo/counter/counter.wasm
	counterWasm []byte
	//go:embed examples/tinygo/counter/index.html
	counterHTML []byte
	//go:embed examples/tinygo/counter/main.go
	counterSrc []byte
)

type Instance struct {
	connLock    sync.Mutex
	connections []io.ReadWriteCloser

	instanceLock  sync.Mutex
	onConnRead    api.Function
	onNewConn     api.Function
	onConnClose   api.Function
	runtime       wazero.Runtime
	networkBuffer []byte

	writeSemaphore *semaphore.Weighted

	cacheDir string
	wasmPath string
}

type instanceNet interface {
	NewConn(c io.ReadWriteCloser) int
	OnConnRead(id int, b []byte)
	OnConnClose(id int)
}

var _ instanceNet = new(Instance)

func NewInstanceX(cacheDir string, wasmPath string) *Instance {
	return &Instance{
		writeSemaphore: semaphore.NewWeighted(4086),
		cacheDir:       cacheDir,
		wasmPath:       wasmPath,
	}
}

func NewInstance(ctx context.Context, src []byte, cacheDir string) (*Instance, error) {
	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(uint32(memoryLimitPages)). // limit to 256kb
		WithCloseOnContextDone(true)                    // ensure we can cancel function calls

	if cacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		runtimeConfig = runtimeConfig.WithCompilationCache(cache)
	}

	i := &Instance{}
	i.runtime = wazero.NewRuntimeWithConfig(ctx, runtimeConfig)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, i.runtime); err != nil {
		return nil, err
	}

	hostModuleBuilder := i.runtime.NewHostModuleBuilder("env")
	builder := hostModuleBuilder.NewFunctionBuilder()
	builder.WithFunc(i.connSend).Export("conn_send")
	builder.WithFunc(i.connClose).Export("conn_close")
	if _, err := hostModuleBuilder.Instantiate(ctx); err != nil {
		return nil, err
	}

	mod, err := i.runtime.InstantiateWithConfig(ctx, counterWasm,
		wazero.NewModuleConfig().
			WithStdout(os.Stdout).
			WithStderr(os.Stderr),
	)
	if err != nil {
		return nil, err
	}
	i.onConnRead = mod.ExportedFunction("on_conn_read")
	if i.onConnRead == nil {
		return nil, fmt.Errorf("on_conn_read function now found")
	}
	i.onNewConn = mod.ExportedFunction("on_new_conn")
	if i.onNewConn == nil {
		return nil, fmt.Errorf("on_new_conn function now found")
	}
	i.onConnClose = mod.ExportedFunction("on_conn_close")
	if i.onConnClose == nil {
		return nil, fmt.Errorf("on_conn_close function now found")
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
	// slog.Info("Instance created")
	return i, nil
}

func (i *Instance) connSend(_ context.Context, m api.Module, connid, offset uint32) (errno uint32) {
	logger := slog.With("connid", connid, "offset", offset)
	logger.Info("conn_send")
	if int(connid) > len(i.connections) {
		// TODO
		logger.Error("connid is too high", "len_connections", len(i.connections))
		return
	}
	conn := i.connections[connid-1]
	if conn == nil {
		// TODO
		logger.Error("conn doesn't exist")
		return
	}
	if offset > uint32(len(i.networkBuffer))-1 {
		// TODO
		logger.Error("offset is larger than network buffer", "len_network_buffer", len(i.networkBuffer))
		return
	}
	// TODO: writes block all execution, make a write pool
	_, err := conn.Write(i.networkBuffer[:offset])
	if err != nil {
		// TODO
		logger.Error("error writing to conn", "err", err)
		return
	}
	return
}

func (i *Instance) connClose(_ context.Context, m api.Module, connid uint32) (errno uint32) {
	logger := slog.With("connid", connid)
	logger.Info("connSend")
	if int(connid) > len(i.connections) {
		// TODO
		logger.Error("connid is too high", "connid", connid, "len_connections", len(i.connections))
		return
	}
	conn := i.connections[connid-1]
	if conn == nil {
		// TODO
		logger.Error("conn doesn't exist", "connid", connid)
		return
	}
	_ = conn.Close()
	// Do not clean up the connection here, wait for the call to OnConnClose
	return
}

func (i *Instance) NewConn(conn io.ReadWriteCloser) int {
	// TODO: do we need this lock, or just when we use the network buffer?
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	id := i.addConnection(conn)
	if _, err := i.onNewConn.Call(context.Background(), uint64(id)); err != nil {
		slog.Error("on_new_conn", "err", err)
	}
	return id
}

func (i *Instance) OnConnRead(id int, b []byte) {
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	ln := copy(i.networkBuffer, b)
	// TODO: when b is larger than networkBuffer
	if _, err := i.onConnRead.Call(context.Background(), uint64(id), uint64(ln)); err != nil {
		slog.Error("on_conn_read", "err", err)
	}
}
func (i *Instance) OnConnClose(id int) {
	// TODO: do we need this lock, or just when we use the network buffer?
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	if _, err := i.onConnClose.Call(context.Background(), uint64(id)); err != nil {
		slog.Error("on_conn_close", "err", err)
	}
	i.removeConnection(id)
}

func (i *Instance) addConnection(conn io.ReadWriteCloser) int {
	i.connLock.Lock()
	defer i.connLock.Unlock()
	for idx, conn := range i.connections {
		if conn == nil {
			i.connections[idx] = conn
			return idx + 1
		}
	}
	i.connections = append(i.connections, conn)
	return len(i.connections)
}

func (i *Instance) removeConnection(id int) {
	i.connLock.Lock()
	defer i.connLock.Unlock()
	i.connections[id-1] = nil
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
		slog.Info("New connection", "conn", conn)
		id := i.NewConn(conn)
		go func() {
			buf := make([]byte, len(i.networkBuffer))
			for {
				ln, err := conn.Read(buf)
				if err != nil {
					i.OnConnClose(id)
					return
				}
				i.OnConnRead(id, buf[:ln])
			}
		}()
	}
}
