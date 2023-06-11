package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"golang.org/x/exp/slog"
	"golang.org/x/sync/semaphore"
)

var (
	memoryLimitPages = 4
	//go:embed examples/tinygo/counter/main.wasm
	counterWasm []byte
	//go:embed examples/tinygo/counter/index.html
	counterHTML []byte
	//go:embed examples/tinygo/counter/main.go
	counterSrc []byte
)

type ConnectionType uint8

const (
	WebsocketConnectionType ConnectionType = iota + 1
	TCPConnectionType
)

type IOEvent uint8

const (
	ReadIOEvent IOEvent = iota + 1
	OpenIOEvent
	CloseIOEvent
)

type Event struct {
	IOEvent IOEvent
	Data    ConnectionType
	Conn    uint32
}

type Instance struct {
	connLock    sync.Mutex
	connections []io.ReadWriteCloser

	eventLock sync.Mutex
	events    []Event

	runtime         wazero.Runtime
	mod             api.Module
	instanceLock    sync.Mutex
	connectionCount int
	onEvent         api.Function
	eventBuffer     []byte
	bufferPtr       uint32
	bufferSize      uint32

	writeSemaphore *semaphore.Weighted

	cacheDir string
	dataDir  string
	stack    [2]uint64
}

func NewInstance(ctx context.Context, cacheDir, dataDir string) (*Instance, error) {
	runtimeConfig := wazero.NewRuntimeConfigCompiler().
		WithMemoryLimitPages(uint32(memoryLimitPages)) // limit to 256kb
		// WithCloseOnContextDone(true)                    // ensure we can cancel function calls

	if cacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		runtimeConfig = runtimeConfig.WithCompilationCache(cache)
	}

	wasmPath := filepath.Join(dataDir, "main.wasm")
	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("main.wasm not found in datadir path %q", wasmPath)
	}

	i := &Instance{
		writeSemaphore: semaphore.NewWeighted(4086),
		cacheDir:       cacheDir,
		dataDir:        dataDir,
		runtime:        wazero.NewRuntimeWithConfig(ctx, runtimeConfig),
	}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, i.runtime); err != nil {
		return nil, err
	}

	hostModuleBuilder := i.runtime.NewHostModuleBuilder("env")
	builder := hostModuleBuilder.NewFunctionBuilder()

	builder.WithGoModuleFunction(api.GoModuleFunc(i.connWrite),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).Export("conn_write")
	builder.WithGoModuleFunction(api.GoModuleFunc(i.connRead),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32}).Export("conn_read")
	builder.WithFunc(i.connClose).Export("conn_close")
	if _, err := hostModuleBuilder.Instantiate(ctx); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *Instance) Start(ctx context.Context) error {
	// This function is guarded by the connectionLock, it is taken during start
	// to prevent double starts or parallel use of the shared buffer

	var err error
	wasmPath := filepath.Join(i.dataDir, "main.wasm")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("main.wasm not found in data dir %q", wasmPath)
	}

	i.mod, err = i.runtime.InstantiateWithConfig(ctx, wasmBytes,
		wazero.NewModuleConfig().
			WithStdout(os.Stdout).
			WithStderr(os.Stderr),
	)
	if err != nil {
		return err
	}

	memFile, err := os.Open(filepath.Join(i.dataDir, "mem"))
	if !os.IsNotExist(err) {
		b, _ := i.mod.Memory().Read(0, i.mod.Memory().Size())
		idx := 0
		for {
			// TODO: Do we just read the whole thing the first time, do we need
			// a loop?
			// TODO: What if the memory size is 4 blocks and the default mod
			// memory is 2?
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
			idx += n
		}
	}

	i.onEvent = i.mod.ExportedFunction("on_event")
	if i.onEvent == nil {
		return fmt.Errorf("on_event function not found")
	}

	if i.bufferPtr == 0 && i.bufferSize == 0 {
		eventBufferFunc := i.mod.ExportedFunction("event_buffer")
		if eventBufferFunc == nil {
			return fmt.Errorf("event_buffer function not found")
		}
		bufferPtrSize, err := i.mod.ExportedFunction("event_buffer").Call(ctx)
		if err != nil {
			return err
		}
		i.bufferPtr = uint32(bufferPtrSize[0] >> 32)
		i.bufferSize = uint32(bufferPtrSize[0])
	}
	i.eventBuffer, _ = i.mod.Memory().Read(i.bufferPtr, i.bufferSize)
	return nil
}

func (i *Instance) Stop(ctx context.Context) error {
	b, _ := i.mod.Memory().Read(0, i.mod.Memory().Size())
	memFile, err := os.Create(filepath.Join(i.dataDir, "mem"))
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

func (i *Instance) connRead(_ context.Context, m api.Module, stack []uint64) {
	id := api.DecodeU32(stack[0])
	offset := api.DecodeU32(stack[1])
	conn := i.getConnection(id)
	if conn == nil {
		// TODO
		// logger.Error("conn doesn't exist")
		return
	}
	if offset > uint32(len(i.eventBuffer))-1 {
		// TODO
		// logger.Error("offset is larger than network buffer", "len_event_buffer", len(i.networkBuffer))
		return
	}
	// TODO: writes block all execution, make a write pool
	_, err := conn.Write(i.eventBuffer[:offset])
	if err != nil {
		// TODO
		// logger.Error("error writing to conn", "err", err)
		return
	}
	// return value
	stack[0] = 0
}

func (i *Instance) connWrite(_ context.Context, m api.Module, stack []uint64) {
	id := api.DecodeU32(stack[0])
	offset := api.DecodeU32(stack[1])
	conn := i.getConnection(id)
	if conn == nil {
		// TODO
		// logger.Error("conn doesn't exist")
		return
	}
	if offset > uint32(len(i.eventBuffer))-1 {
		// TODO
		// logger.Error("offset is larger than network buffer", "len_event_buffer", len(i.networkBuffer))
		return
	}
	// TODO: writes block all execution, make a write pool
	_, err := conn.Write(i.eventBuffer[:offset])
	if err != nil {
		// TODO
		// logger.Error("error writing to conn", "err", err)
		return
	}
	// return value
	stack[0] = 0
}

func (i *Instance) connClose(_ context.Context, m api.Module, connid uint32) (errno uint32) {
	logger := slog.With("connid", connid)
	logger.Info("connSend")
	conn := i.getConnection(connid)
	if conn == nil {
		// TODO
		logger.Error("conn doesn't exist", "connid", connid)
		return
	}
	_ = conn.Close()
	// Do not clean up the connection here, wait for the call to OnConnClose
	return
}

func (i *Instance) addEvent(e Event) {
	i.eventLock.Lock()
	defer i.eventLock.Unlock()
	i.events = append(i.events, e)
}

func (i *Instance) sendEvents(ctx context.Context) {
	// i.instanceLock should be acquired during this call
	i.eventLock.Lock()
	events := i.events
	i.events = nil
	i.eventLock.Unlock()
	if len(events) == 0 {
		return
	}

	// TODO: serialize into event buffer
	_ = events
	// TODO: do we need this lock or does wazero have a lock?
	// TODO: is the lock for the shared buffer?
	i.instanceLock.Lock()
	defer i.instanceLock.Unlock()
	if _, err := i.onEvent.Call(ctx, 0, 0); err != nil {
		slog.Error("on_event", "err", err)
	}
}

func (i *Instance) NewConn(ctx context.Context, typ ConnectionType, conn io.ReadWriteCloser) (uint32, error) {
	id, err := i.addConnection(ctx, conn)
	if err != nil {
		return 0, err
	}
	i.addEvent(Event{
		IOEvent: OpenIOEvent,
		Conn:    id,
		Data:    typ,
	})
	i.sendEvents(ctx)
	return id, nil
}

func (i *Instance) OnConnRead(ctx context.Context, id uint32) error {
	i.stack[0] = uint64(id)
	i.addEvent(Event{
		IOEvent: ReadIOEvent,
		Conn:    id,
	})
	i.sendEvents(ctx)
	return nil
}
func (i *Instance) OnConnClose(ctx context.Context, id uint32) error {
	i.addEvent(Event{
		IOEvent: CloseIOEvent,
		Conn:    id,
	})
	i.sendEvents(ctx)
	return nil
}

func (i *Instance) getConnection(id uint32) io.ReadWriteCloser {
	if int(id) > len(i.connections) {
		return nil
	}
	return i.connections[id-1]
}

func (i *Instance) addConnection(ctx context.Context, conn io.ReadWriteCloser) (uint32, error) {
	i.connLock.Lock()
	defer i.connLock.Unlock()
	if i.connectionCount == 0 {
		if err := i.Start(ctx); err != nil {
			return 0, err
		}
	}
	i.connectionCount++ // add to live connection count
	for idx, c := range i.connections {
		if c == nil {
			i.connections[idx] = conn
			return uint32(idx + 1), nil
		}
	}
	// No empty slots, add it to the end and use the len as the id
	i.connections = append(i.connections, conn)
	return uint32(len(i.connections)), nil
}

func (i *Instance) removeConnection(ctx context.Context, id uint32) error {
	i.connLock.Lock()
	defer i.connLock.Unlock()
	i.connections[id-1] = nil
	i.connectionCount--
	if i.connectionCount == 0 {
		return i.Stop(ctx)
	}
	return nil
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
		asyncConn := &asyncReadConn{Conn: conn}
		connID, err := i.NewConn(ctx, TCPConnectionType, asyncConn)
		if err != nil {
			return err
		}
		go asyncConn.ReadLoop(ctx, connID, i)
	}
}

type asyncReadConn struct {
	net.Conn
	lock sync.Mutex
	buf  bytes.Buffer
}

func (a *asyncReadConn) Read(b []byte) (n int, err error) {
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.buf.Len() == 0 {
		return 0, syscall.EAGAIN
	}
	return a.buf.Read(b)
}

func (a *asyncReadConn) ReadLoop(ctx context.Context, connID uint32, i *Instance) {
	buf := make([]byte, 4096)
	for {
		ln, err := a.Conn.Read(buf)
		if err != nil {
			_ = i.OnConnClose(ctx, connID)
			return
		}
		a.lock.Lock()
		_, _ = a.buf.Write(buf[:ln])
		a.lock.Unlock()
		// Notify that we have a read
		_ = i.OnConnRead(ctx, connID)
	}
}
