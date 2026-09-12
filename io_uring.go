package main

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
	"os"
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
		if r.cq.ringSz > r.sq.ringSz {
			r.sq.ringSz = r.cq.ringSz
		}
		r.cq.ringSz = r.sq.ringSz
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

func main() {
	var r ring
	errno := setup(2, &r, 0)
	println(errno)

	f, err := os.Create("./example.txt")
	if err != nil {
		fmt.Printf("error creating file: %v\n", err)
		os.Exit(1)
	}

	txt := []byte("Hello World!\n")

	submit_to_sq(&r, 23, int32(f.Fd()), uintptr(unsafe.Pointer(&txt[0])), uint32(len(txt)), 0)
}

type (
	sqe struct {
		opcode   uint8  /* type of operation for this sqe */
		flags    uint8  /* IOSQE_ flags */
		ioprio   uint16 /* ioprio for the request */
		fd       int32  /* file descriptor to do IO on */
		off      uint64 /* offset into file */
		addr     uint64 /* pointer to buffer or iovecs */
		len      uint32 /* buffer size or number of iovecs */
		sqeFlags uint32
		userData uint64 /* data to be passed back at completion time */
		pad      [3]uint64
	}

	sQueue struct {
		khead        *uint32
		ktail        *uint32
		kringMask    *uint32
		kringEntries *uint32
		kflags       *uint32
		kdropped     *uint32
		array        []uint32
		sqes         []sqe
		sqeHead      uint32
		sqeTail      uint32
		ringSz       uint32
		sqRingFd     unsafe.Pointer
		pad          [4]uint32
	}

	// IO completion data structure (Completion Queue Entry)
	cqe struct {
		userData uint64
		res      int32
		flags    uint32
	}

	cQueue struct {
		khead        *uint32
		ktail        *uint32
		kringMask    *uint32
		kringEntries *uint32
		kflags       *uint32
		koverflow    *uint32
		cqes         []cqe
		ringSz       uint32
		cqRingFd     unsafe.Pointer
		pad          [4]uint32
	}

	// offsets for mmap
	ioSqOffsets struct {
		head        uint32
		tail        uint32
		ringMask    uint32
		ringEntries uint32
		flags       uint32
		dropped     uint32
		array       uint32
		resv1       uint32
		resv2       uint64
	}

	ioCqOffsets struct {
		head        uint32
		tail        uint32
		ringMask    uint32
		ringEntries uint32
		overflow    uint32
		cqes        uint32
		resv        [2]uint64
	}

	ioParams struct {
		sqEntries    uint32
		cqEntries    uint32
		flags        uint32
		sqThreadCPU  uint32
		sqThreadIdle uint32
		features     uint32
		resv         [4]uint32
		sqOff        ioSqOffsets
		cqOff        ioCqOffsets
	}

	ring struct {
		sq       sQueue
		cq       cQueue
		flags    uint32
		ringFd   int
		features uint32
	}

	cqUserData struct {
		buf syscall.Iovec
		cap int
	}
)
