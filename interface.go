package io_uring

import (
	"syscall"
	"unsafe"
)

const DefaultEntries = 256 

func NewRingDefault() (*Ring, syscall.Errno) {
	ring := new(Ring)
	errno := setup(DefaultEntries, ring, 0)
	return ring, errno
}

func NewRingWithEntries(entries uint32) (*Ring, syscall.Errno) {
	ring := new(Ring)
	errno := setup(entries, ring, 0)
	return ring, errno
}

func (r *Ring) ListenCQ(callback func(CQE)) {
	for {
		_, err := enter(r.ringFd, 0, 1, uint32(ioringEnterGetEvents))
		if err != 0 {
			continue
		}

		for {
			cqe, ok := r.readFromCQ()
			if !ok {
				break
			}
			callback(cqe)
		}
	}
}

func (r *Ring) SubmitRead(fd uintptr, buf []byte, data uint64) int {
	return r.submitToSQ(opRead, int32(fd), uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), data)
}

func (r *Ring) SubmitWrite(fd uintptr, buf []byte, data uint64) int {
	return r.submitToSQ(opWrite, int32(fd), uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), data)
}
