package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"runtime/trace"
)

func simpleMemoryRestore(ctx context.Context, tmpDir string) error {
	region := trace.StartRegion(ctx, "newInstance")
	i, err := NewInstance(ctx, tmpDir, "", counterWasm)
	if err != nil {
		return err
	}
	region.End()
	region = trace.StartRegion(ctx, "startInstance")
	if err := i.Start(ctx); err != nil {
		return err
	}
	region.End()
	trace.WithRegion(ctx, "writeReadClose", func() {
		conn := &readWriteCloser{Buffer: bytes.Buffer{}}
		connID := i.NewConn(conn)
		i.OnConnRead(connID, []byte("hi"))

		if conn.Buffer.String() != "01" {
			panic(fmt.Errorf("unexpected byte value, got %q, expecting %q", conn.Buffer.String(), "01"))
		}
		i.OnConnClose(connID)
	})

	if err := i.Stop(ctx); err != nil {
		return err
	}
	region = trace.StartRegion(ctx, "startInstance2")
	if err := i.Start(ctx); err != nil {
		return err
	}
	region.End()
	{
		conn := &readWriteCloser{Buffer: bytes.Buffer{}}
		connID := i.NewConn(conn)
		i.OnConnRead(connID, []byte("hi"))
		fmt.Println(conn.Buffer.String())
		if conn.Buffer.String() != "12" {
			return fmt.Errorf("unexpected byte value, got %q, expecting %q", conn.Buffer.String(), "12")
		}
		i.OnConnClose(connID)
	}
	if err := i.Stop(ctx); err != nil {
		return err
	}
	os.Remove(filepath.Join(tmpDir, "mem"))
	i.runtime.Close(ctx)
	return nil
}

func TestMemoryRestore(t *testing.T) {
	if err := simpleMemoryRestore(context.Background(), t.TempDir()); err != nil {
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

func BenchmarkRestore(b *testing.B) {
	tmpDir := b.TempDir()
	for i := 0; i < b.N; i++ {
		if err := simpleMemoryRestore(context.Background(), tmpDir); err != nil {
			b.Fatal(err)
		}
		runtime.GC()
	}
}
