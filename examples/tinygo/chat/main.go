package main

import (
	"strconv"
	"unsafe"
)

var networkBuffer [1024]byte

var members = map[uint32]struct{}{}

func main() {}

//export network_buffer
func network_buffer() (ptrSize uint64) {
	p := unsafe.Pointer(&networkBuffer)
	ptr, size := uint32(uintptr(p)), uint32(len(networkBuffer))
	return (uint64(ptr) << uint64(32)) | uint64(size)
}

//export conn_shutdown
func conn_shutdown(connid uint32) uint32

//export conn_write
func conn_write(connid, offset uint32) uint32

//export conn_closed
func conn_closed(connid uint32) {}

//export conn_recv
func conn_recv(connid, offset uint32) {
	strInt := strconv.Itoa(int(connid))
	if _, found := members[connid]; !found {
		members[connid] = struct{}{}
		len := copy(networkBuffer[0:], []byte("0: welcome, you are connection "+strInt+"\n"))

		conn_write(connid, uint32(len))
		len = copy(networkBuffer[0:], []byte("connection "+strInt+" joined the chat\n"))
		for member := range members {
			if member != connid {
				conn_write(member, uint32(len))
			}
		}
	}
	// Send message to other chat members
	label := strInt + ": "
	copy(networkBuffer[len(label):], networkBuffer[:offset])
	copy(networkBuffer[0:], []byte(label))
	for member := range members {
		if member != connid {
			conn_write(member, offset+uint32(len(label)))
		}
	}
}
