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

//export on_conn_close
func on_conn_close(connid uint32) { delete(members, connid) }

//export on_new_conn
func on_new_conn(connid uint32) uint32 {
	members[connid] = struct{}{}
	sendCounter(connid)
	return 0
}

func sendCounter(connid uint32) {
	ln := copy(networkBuffer[0:], []byte(fmt.Sprint(counter)))
	conn_send(connid, uint32(ln))
}

//export on_conn_read
func on_conn_read(connid, offset uint32) uint32 {
	counter++
	// If we get any bytes, increment the counter and send it out
	for member := range members {
		sendCounter(member)
	}
	return 0
}

//export conn_close
func conn_close(connid uint32) uint32

//export conn_send
func conn_send(connid, offset uint32) uint32
