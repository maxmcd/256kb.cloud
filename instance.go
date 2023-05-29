package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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

	runtime       wazero.Runtime
	mod           api.Module
	instanceLock  sync.Mutex
	onConnRead    api.Function
	onNewConn     api.Function
	onConnClose   api.Function
	networkBuffer []byte
	bufferPtr     uint32
	bufferSize    uint32

	writeSemaphore *semaphore.Weighted

	cacheDir  string
	wasmPath  string
	wasmBytes []byte
}

func NewInstance(ctx context.Context, cacheDir, wasmPath string, wasmBytes []byte) (*Instance, error) {
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

	i := &Instance{
		writeSemaphore: semaphore.NewWeighted(4086),
		cacheDir:       cacheDir,
		wasmPath:       wasmPath,
		wasmBytes:      wasmBytes,
		runtime:        wazero.NewRuntimeWithConfig(ctx, runtimeConfig),
	}

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
	return i, nil
}

func (i *Instance) Start(ctx context.Context) error {
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	var err error
	i.mod, err = i.runtime.InstantiateWithConfig(ctx, counterWasm,
		wazero.NewModuleConfig().
			WithStdout(os.Stdout).
			WithStderr(os.Stderr),
	)
	if err != nil {
		return err
	}

	memFile, err := os.Open(filepath.Join(i.cacheDir, "mem"))
	if !os.IsNotExist(err) {
		b, _ := i.mod.Memory().Read(0, i.mod.Memory().Size())
		idx := 0
		for {
			// TODO: Do we just read the whole thing the first time, do we need a loop?
			// What if the memory size is 4 blocks and the default mod memory is 2?
			n, err := memFile.Read(b[idx:])
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if uint32(idx+n) == i.mod.Memory().Size() {
				break
			}
		}
	}

	i.onConnRead = i.mod.ExportedFunction("on_conn_read")
	if i.onConnRead == nil {
		return fmt.Errorf("on_conn_read function now found")
	}
	i.onNewConn = i.mod.ExportedFunction("on_new_conn")
	if i.onNewConn == nil {
		return fmt.Errorf("on_new_conn function now found")
	}
	i.onConnClose = i.mod.ExportedFunction("on_conn_close")
	if i.onConnClose == nil {
		return fmt.Errorf("on_conn_close function now found")
	}
	if i.bufferPtr == 0 && i.bufferSize == 0 {
		networkBufferFunc := i.mod.ExportedFunction("network_buffer")
		if networkBufferFunc == nil {
			return fmt.Errorf("network_buffer function now found")
		}
		bufferPtrSize, err := i.mod.ExportedFunction("network_buffer").Call(ctx)
		if err != nil {
			return err
		}
		i.bufferPtr = uint32(bufferPtrSize[0] >> 32)
		i.bufferSize = uint32(bufferPtrSize[0])
	}
	i.networkBuffer, _ = i.mod.Memory().Read(i.bufferPtr, i.bufferSize)
	return nil
}

func (i *Instance) Stop(ctx context.Context) error {
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	b, _ := i.mod.Memory().Read(0, i.mod.Memory().Size())
	memFile, err := os.Create(filepath.Join(i.cacheDir, "mem"))
	if err != nil {
		return err
	}
	if _, err := memFile.Write(b); err != nil {
		return err
	}
	if err := memFile.Close(); err != nil {
		return err
	}
	return i.mod.Close(ctx)
}

func (i *Instance) connSend(_ context.Context, m api.Module, connid, offset uint32) (errno uint32) {
	logger := slog.With("connid", connid, "offset", offset)
	logger.Info("conn_send")
	conn := i.getConnection(int(connid))
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
	conn := i.getConnection(int(connid))
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

func (i *Instance) getConnection(id int) io.ReadWriteCloser {

	if int(id) > len(i.connections) {
		return nil
	}
	return i.connections[id-1]
}

func (i *Instance) addConnection(conn io.ReadWriteCloser) int {
	i.connLock.Lock()
	defer i.connLock.Unlock()
	for idx, c := range i.connections {
		if c == nil {
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
