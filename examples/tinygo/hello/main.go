package main

import (
	"fmt"
	"strconv"
	"unsafe"
)

var data [1024]byte

var members = map[uint32]struct{}{}

func main() {}

//export network_buffer
func network_buffer() (ptrSize uint64) {
	p := unsafe.Pointer(&data)
	ptr, size := uint32(uintptr(p)), uint32(len(data))
	// // c := 2
	// // thing := []byte{}
	// // for {
	// // 	fmt.Println(c)
	// // 	thing = append(thing, bytes.Repeat([]byte{'h'}, c)...)
	// // 	runtime.GC()
	// // 	c *= 2
	// // }
	foo := make([]byte, (32768*2*2)+24000, 32768*2*2+24000)
	fmt.Println(len(foo), cap(foo))
	return (uint64(ptr) << uint64(32)) | uint64(size)
}

//export conn_shutdown
func conn_shutdown(connid uint32) uint32

//export conn_send
func conn_send(connid, offset uint32) uint32

//export conn_closed
func conn_closed(connid uint32) {}

//export conn_recv
func conn_recv(connid, offset uint32) {
	strInt := strconv.Itoa(int(connid))
	if _, found := members[connid]; !found {
		members[connid] = struct{}{}
		len := copy(data[0:], []byte("0: welcome, you are connection "+strInt+"\n"))

		conn_send(connid, uint32(len))
		len = copy(data[0:], []byte("connection "+strInt+" joined the chat\n"))
		for member := range members {
			if member != connid {
				conn_send(member, uint32(len))
			}
		}
	}
	// Send message to other chat members
	label := strInt + ": "
	copy(data[len(label):], data[:offset])
	copy(data[0:], []byte(label))
	for member := range members {
		if member != connid {
			conn_send(member, offset+uint32(len(label)))
		}
	}
}
