package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type T interface {
	Fatal(args ...any)
	Error(args ...any)
	TempDir() string
	Helper()
}

var _ T = new(testing.T)
var _ T = new(testing.B)

func requireEqual[A any](t T, expected A, actual A) {
	if !reflect.DeepEqual(expected, actual) {
		t.Helper()
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
	for _, expected := range []string{"01", "12"} {
		conn := &readWriteCloser{Reader: &bytes.Buffer{}, Writer: &bytes.Buffer{}}
		connID, err := i.NewConn(ctx, TCPConnectionType, conn)
		if err != nil {
			t.Fatal(err)
		}
		requireEqual(t, 1, i.connectionCount)
		conn.Reader.(*bytes.Buffer).Write([]byte("hi"))
		i.OnConnRead(ctx, connID)
		requireEqual(t, expected, conn.Writer.(*bytes.Buffer).String())
		i.OnConnClose(ctx, connID)
		requireEqual(t, 0, i.connectionCount)
	}

	_ = os.Remove(filepath.Join(tmpDir, "mem"))
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
	io.Reader
	io.Writer
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

func FuzzEventSerialize(f *testing.F) {
	deserialize := func(b []byte) []Event {
		events := []Event{}
		for i := 0; i < len(b)/6; i++ {
			events = append(events, Event{
				IOEvent: IOEvent(b[i*6]),
				Conn:    binary.LittleEndian.Uint32(b[(i*6)+2 : (i*6)+6]),
				Data:    ConnectionType(b[(i*6)+1]),
			})
		}
		return events
	}
	f.Add([]byte{1, 2, 1, 0, 0, 0}, uint(6))
	f.Add([]byte{1, 2, 1, 0, 0, 0, 1, 2, 3, 0, 0, 0}, uint(8))
	f.Add([]byte{1, 2, 1, 0, 0, 0, 1, 2, 3, 0, 0, 0}, uint(6))
	f.Add([]byte("0"), uint(99))
	f.Add([]byte("\x01\x02\x01\x00\x00\x00\xf0\x00"), uint(5))
	f.Add([]byte("000000000000000000"), uint(11))
	f.Fuzz(func(t *testing.T, b []byte, i uint) {
		t.Log(i, b)
		events := deserialize(b)
		if i < 6 {
			return
		}
		other := make([]byte, i)
		var ln int
		var remainingEvents []Event
		for x := 0; x < 5; x++ {
			ln, remainingEvents = serializeEvents(other, events)
			t.Log(ln, remainingEvents, other, events)
			requireEqual(t, events[:len(events)-len(remainingEvents)], deserialize(other[:ln]))
			requireEqual(t, ln, (len(events)-len(remainingEvents))*6)
			if len(remainingEvents) == 0 {
				break
			}
			if len(remainingEvents) > 0 {
				events = remainingEvents
			}
		}

	})
}
