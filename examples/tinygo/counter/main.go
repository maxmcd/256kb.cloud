// You can edit this code!
// Click here and start typing.
package main

import (
	"fmt"
	"unsafe"
)

var (
	networkBuffer [1024]byte
	members       = map[uint32]struct{}{}
	counter       int
)

func main() {} // required, but unused

//export network_buffer
func network_buffer() (ptrSize uint64) {
	p := unsafe.Pointer(&networkBuffer)
	ptr, size := uint32(uintptr(p)), uint32(len(networkBuffer))
	return (uint64(ptr) << uint64(32)) | uint64(size)
}

//export conn_closed
func conn_closed(connid uint32) { delete(members, connid) }

//export conn_accept
func conn_accept(connid uint32) uint32 {
	members[connid] = struct{}{}
	sendCounter(connid)
	return 0
}

func sendCounter(connid uint32) {
	ln := copy(networkBuffer[0:], []byte(fmt.Sprint(counter)))
	conn_send(connid, uint32(ln))
}

//export conn_recv
func conn_recv(connid, offset uint32) {
	// If we get any bytes, increment the counter and send it out
	for member := range members {
		sendCounter(member)
	}
}

//export conn_close
func conn_close(connid uint32) uint32

//export conn_send
func conn_send(connid, offset uint32) uint32
