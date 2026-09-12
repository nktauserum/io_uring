package io_uring

// #include <signal.h>
// #include <syscall.h>
//
// int nsig() {
//     return _NSIG;
// }
//
// int syscall_nums(int *io_uring_setup, int *io_uring_enter) {
// #if defined(__NR_io_uring_setup) && defined(__NR_io_uring_enter)
//     *io_uring_setup = __NR_io_uring_setup;
//     *io_uring_enter = __NR_io_uring_enter;
// #else
//	   *io_uring_setup = -1;
//	   *io_uring_enter = -1;
// #endif
// }
import "C"

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	ioringOffSqRing      = uint64(0x0)
	ioringOffCqRing      = uint64(0x8000000)
	ioringOffSqes        = uint64(0x10000000)
	ioringFeatSingleMmap = uint32(0x1)
	ioringEnterGetEvents = uint64(1) << 0
)

var nSig, ioUringSetupSys, ioUringEnterSys = func() (int, int, int) {
	nSig := int(C.nsig())
	var ioSetupSys, ioEnterSys C.int
	C.syscall_nums(&ioSetupSys, &ioEnterSys)
	if ioSetupSys == -1 || ioEnterSys == -1 {
		panic("io_uring is not supported")
	}

	return nSig, int(ioSetupSys), int(ioEnterSys)
}()

func unmap(sq *sQueue, cq *cQueue) {
	_, _, _ = syscall.RawSyscall(syscall.SYS_MUNMAP, uintptr(sq.sqRingFd), uintptr(sq.ringSz), 0)
	if cq.cqRingFd != nil && cq.cqRingFd != sq.sqRingFd {
		_, _, _ = syscall.RawSyscall(syscall.SYS_MUNMAP, uintptr(cq.cqRingFd), uintptr(cq.ringSz), 0)
	}
}

func enter(ringFd int, toSubmit, minComplete, flags uint32) syscall.Errno {
	_, _, err := syscall.RawSyscall6(uintptr(ioUringEnterSys), uintptr(ringFd), uintptr(toSubmit), uintptr(minComplete), uintptr(flags), 0, 0)
	return err
}

func setup(entries uint32, r *ring, flags uint32) syscall.Errno {
	var p ioParams
	p.flags = flags

	r1, _, err := syscall.RawSyscall(uintptr(ioUringSetupSys), uintptr(entries), uintptr(unsafe.Pointer(&p)), 0)
	if err != 0 {
		fmt.Printf("error syscall setup: %v\n", err)
		return err
	}

	r.ringFd = int(r1)
	r.sq.ringSz = p.sqOff.array + p.sqEntries*uint32(unsafe.Sizeof(uint32(0)))
	r.cq.ringSz = p.cqOff.cqes + p.cqEntries*uint32(unsafe.Sizeof(cqe{}))

	sqPtr, _, err := syscall.RawSyscall6(
		syscall.SYS_MMAP, 0,
		uintptr(r.sq.ringSz),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE,
		uintptr(r.ringFd),
		uintptr(ioringOffSqRing))
	if err != 0 {
		fmt.Printf("error mmap syscall: %v\n", err)
		return err
	}
	r.sq.sqRingFd = unsafe.Pointer(sqPtr)

	if p.features&ioringFeatSingleMmap != 0 {
		r.cq.cqRingFd = r.sq.sqRingFd
	} else {
		cqPtr, _, e := syscall.RawSyscall6(
			syscall.SYS_MMAP,
			0,
			uintptr(r.cq.ringSz),
			syscall.PROT_READ|syscall.PROT_WRITE,
			syscall.MAP_SHARED|syscall.MAP_POPULATE,
			uintptr(r.ringFd),
			uintptr(ioringOffCqRing))
		if e != 0 {
			unmap(&r.sq, &r.cq)
			return err
		}
		r.cq.cqRingFd = unsafe.Pointer(cqPtr)
	}

	sq := &r.sq
	sq.khead = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.head)))
	sq.ktail = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.tail)))
	sq.kringMask = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.ringMask)))
	sq.kringEntries = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.ringEntries)))
	sq.kflags = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.flags)))
	sq.kdropped = (*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.dropped)))

	arr := unsafe.Slice(
		(*uint32)(unsafe.Pointer(unsafe.Add(r.sq.sqRingFd, p.sqOff.array))),
		int(p.sqEntries))
	sq.array = arr

	sqes, _, e := syscall.RawSyscall6(
		syscall.SYS_MMAP,
		0,
		uintptr(p.sqEntries*uint32(unsafe.Sizeof(sqe{}))),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE,
		uintptr(r.ringFd),
		uintptr(ioringOffSqes))
	if e != 0 {
		unmap(&r.sq, &r.cq)
		return e
	}
	sqeSlice := unsafe.Slice((*sqe)(unsafe.Pointer(sqes)), int(p.sqEntries))
	sq.sqes = sqeSlice

	cq := &r.cq
	cq.khead = (*uint32)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.head)))
	cq.ktail = (*uint32)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.tail)))
	cq.kringMask = (*uint32)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.ringMask)))
	cq.kringEntries = (*uint32)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.ringEntries)))
	cq.koverflow = (*uint32)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.overflow)))

	cqeSlice := unsafe.Slice((*cqe)(unsafe.Pointer(unsafe.Add(r.cq.cqRingFd, p.cqOff.cqes))), int(p.cqEntries))
	cq.cqes = cqeSlice

	r.features = p.features

	return 0
}

func submit_to_sq(r *ring, op uint8, fd int32, addr uintptr, len uint32, offset uint64) {
	tail := atomic.LoadUint32(r.sq.ktail)
	index := tail & atomic.LoadUint32(r.sq.kringMask)

	sqe := &r.sq.sqes[index]
	sqe.opcode = op
	sqe.fd = fd
	sqe.addr = uint64(addr)
	sqe.len = len
	sqe.off = offset

	r.sq.array[index] = index
	tail += 1

	atomic.StoreUint32(r.sq.ktail, tail)

	err := enter(r.ringFd, 1, 1, uint32(ioringEnterGetEvents))
	if err != 0 {
		fmt.Printf("enter: %v\n", err)
	}
}

func read_from_cq(r *ring) (int32, bool) {
	head := atomic.LoadUint32(r.cq.khead)
	
	if head == atomic.LoadUint32(r.cq.ktail) {
		return -1, false // empty buffer
	}

	cqe := r.cq.cqes[head & atomic.LoadUint32(r.cq.kringMask)]
	if cqe.res < 0 {
		return cqe.res, false
	}

	head += 1

	atomic.StoreUint32(r.cq.khead, head)

	return cqe.res, true
}
