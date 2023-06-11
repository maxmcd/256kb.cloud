package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type T interface {
	Fatal(args ...any)
	Error(args ...any)
	TempDir() string
}

var _ T = new(testing.T)
var _ T = new(testing.B)

func requireEqual[A comparable](t T, expected A, actual A) {
	if expected != actual {
		t.Fatal(fmt.Sprintf("Expected value '%v', but got '%v'", expected, actual))
	}
}

func simpleMemoryRestore(t T, ctx context.Context, tmpDir string) error {
	if err := os.WriteFile(filepath.Join(tmpDir, "main.wasm"), counterWasm, 0666); err != nil {
		return err
	}
	i, err := NewInstance(ctx, tmpDir, tmpDir)
	if err != nil {
		return err
	}
	{
		conn := &readWriteCloser{Buffer: bytes.Buffer{}}
		connID, err := i.NewConn(ctx, TCPConnectionType, conn)
		if err != nil {
			t.Fatal(err)
		}
		requireEqual(t, 1, i.connectionCount)
		conn.Buffer.Write([]byte("hi"))
		i.OnConnRead(ctx, connID)
		requireEqual(t, conn.Buffer.String(), "01")
		i.OnConnClose(ctx, connID)
	}
	requireEqual(t, 0, i.connectionCount)
	{
		conn := &readWriteCloser{Buffer: bytes.Buffer{}}
		connID, err := i.NewConn(ctx, TCPConnectionType, conn)
		if err != nil {
			t.Fatal(err)
		}
		requireEqual(t, 1, i.connectionCount)
		conn.Buffer.Write([]byte("hi"))
		i.OnConnRead(ctx, connID)
		requireEqual(t, conn.Buffer.String(), "12")
		i.OnConnClose(ctx, connID)
	}
	os.Remove(filepath.Join(tmpDir, "mem"))
	i.runtime.Close(ctx)
	return nil
}

func TestMemoryRestore(t *testing.T) {
	if err := simpleMemoryRestore(t, context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := simpleMemoryRestore(t, context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

var _ io.ReadWriteCloser = new(readWriteCloser)

type readWriteCloser struct {
	bytes.Buffer
}

func (r *readWriteCloser) Close() error {
	return nil
}

// func BenchmarkRestore(b *testing.B) {
// 	tmpDir := b.TempDir()

// 	for i := 0; i < b.N; i++ {
// 		if err := simpleMemoryRestore(b, context.Background(), tmpDir); err != nil {
// 			b.Fatal(err)
// 		}
// 	}
// }

// func BenchmarkRead(b *testing.B) {
// 	var t T = b
// 	dataDir := b.TempDir()
// 	if err := os.WriteFile(filepath.Join(dataDir, "main.wasm"), counterWasm, 0777); err != nil {
// 		t.Fatal(err)
// 	}
// 	i, err := NewInstance(context.Background(), b.TempDir(), dataDir)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	type rwc struct {
// 		io.Reader
// 		io.Writer
// 		io.Closer
// 	}
// 	connID, err := i.NewConn(context.Background(),
// 		rwc{Reader: bytes.NewBuffer(nil), Writer: io.Discard, Closer: io.NopCloser(bytes.NewBuffer(nil))})
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	writeBytes := []byte("words")
// 	for x := 0; x < b.N; x++ {
// 		for y := 0; y < 100; y++ {
// 			i.OnConnRead(connID, writeBytes)
// 		}
// 	}
// }

func TestOnEventParsing(t *testing.T) {

}
