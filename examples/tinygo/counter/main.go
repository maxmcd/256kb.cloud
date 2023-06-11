// You can edit this code!
// Click here and start typing.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"syscall"
	"unsafe"
)

type App struct {
	members map[Conn]struct{}
	counter int
}

var app = &App{members: map[Conn]struct{}{}}

func (a *App) OnOpen(c Conn, ct ConnectionType) {
	if ct != WebsocketConnectionType {
		_ = c.Close()
	}
	a.members[c] = struct{}{}
	sendCounter(c, a.counter)
}

func (a *App) OnData(c Conn) {
	_, _ = io.Copy(io.Discard, c)
	a.counter++
	// If we get any bytes, increment the counter and send it out
	for member := range a.members {
		sendCounter(member, a.counter)
	}
}

func (a *App) OnClose(c Conn) {
	delete(a.members, c)
}

func sendCounter(c Conn, counter int) {
	ln := copy(eventBuffer[0:], []byte(fmt.Sprint(counter)))
	c.Write(eventBuffer[:ln])
}

type Conn uint32

func (c Conn) Close() error {
	_ = conn_close(uint32(c))
	return nil
}

func (c Conn) Write(b []byte) (n int, err error) {
	p := unsafe.Pointer(&b)
	ptr, size := uint32(uintptr(p)), uint32(len(b))
	resp := conn_write(uint32(c), ptr, size)
	n = int(uint32(resp >> 32))
	if uint32(resp) != 0 {
		return n, syscall.Errno(uint32(resp))
	}
	return n, nil
}

func (c Conn) Read(b []byte) (n int, err error) {
	p := unsafe.Pointer(&b)
	ptr, size := uint32(uintptr(p)), uint32(len(b))
	resp := conn_read(uint32(c), ptr, size)
	n = int(uint32(resp >> 32))
	if uint32(resp) != 0 {
		return n, syscall.Errno(uint32(resp))
	}
	return n, nil
}

func main() {} // required, but unused

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

var eventBuffer [256]byte

//export event_buffer
func event_buffer() (ptrSize uint64) {
	p := unsafe.Pointer(&eventBuffer)
	ptr, size := uint32(uintptr(p)), uint32(len(eventBuffer))
	return (uint64(ptr) << uint64(32)) | uint64(size)
}

//export on_event
func on_event(offset uint32) {
	buf := eventBuffer[:offset]
	for i := 0; i < len(buf)/6; i++ {
		event := IOEvent(buf[i*6])
		c := Conn(binary.BigEndian.Uint32(buf[(i*6)+2 : (i*6)+6]))
		switch event {
		case ReadIOEvent:
			app.OnData(c)
		case OpenIOEvent:
			app.OnOpen(c, ConnectionType(buf[(i*6)+1]))
		case CloseIOEvent:
			app.OnClose(c)
		}
	}
}

//export conn_close
func conn_close(connid uint32) uint32

//export conn_read
func conn_read(connid, ptr, offset uint32) uint32

//export conn_write
func conn_write(connid, ptr, offset uint32) uint32
