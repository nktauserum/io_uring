package io_uring

import (
	"os"
	"testing"
	"time"
	"unsafe"
)

const wanted = "Hello World!"
const wait_time = 50 * time.Millisecond

func TestSetup(t *testing.T) {
	var r Ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fail()
	}
}

func TestSubmitToSQ(t *testing.T) {
	var r Ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fatalf("Error setup io_uring: code %v\n", errno)
	}

	f, err := os.Create("./example.txt")
	if err != nil {
		t.Fatalf("error creating file: %v\n", err)
	}
	defer f.Close()
	defer os.Remove("./example.txt")

	txt := []byte(wanted)

	ret := r.submitToSQ(23, int32(f.Fd()), uintptr(unsafe.Pointer(&txt[0])), uint32(len(txt)), 0)
	t.Logf("Submitted %v events to SQ\n", ret)

	time.Sleep(wait_time)

	_, ok := r.readFromCQ()
	if !ok {
		t.Fail()
	}

	buf := make([]byte, 1024)
	ret = r.submitToSQ(22, int32(f.Fd()), uintptr(unsafe.Pointer(&buf[0])), 1024, 0)
	t.Logf("Submitted %v events to SQ\n", ret)

	time.Sleep(wait_time)

	if string(buf[:len(wanted)]) != wanted {
		t.Fatalf("Error: got %v, wanted %v\n", string(buf), wanted)
	}
}

func TestListenCQ(t *testing.T) {
	var r Ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fatalf("Error setup io_uring: code %v\n", errno)
	}

	f, err := os.Create("./example_listen.txt")
	if err != nil {
		t.Fatalf("error creating file: %v\n", err)
	}
	defer f.Close()

	txt := []byte(wanted)

	ret := r.submitToSQ(23, int32(f.Fd()), uintptr(unsafe.Pointer(&txt[0])), uint32(len(txt)), 0)
	t.Logf("Submitted %v events to SQ\n", ret)

	ch := make(chan cqe)
	go r.ListenCQ(ch)

	cqe := <-ch
	t.Logf("Write %v bytes to file\n", cqe.res)

	buf := make([]byte, 1024)
	r.SubmitRead(f.Fd(), buf)
	<-ch
	if string(buf[:len(wanted)]) != wanted {
		t.Fatalf("Error: got %v, wanted %v\n", string(buf), wanted)
	}

}

func TestSubmitWrite(t *testing.T) {
	var r Ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fatalf("Error setup io_uring: code %v\n", errno)
	}

	f, err := os.Create("./example_listen.txt")
	if err != nil {
		t.Fatalf("error creating file: %v\n", err)
	}
	defer f.Close()

	txt := []byte(wanted)

	ret := r.SubmitWrite(f.Fd(), txt)
	t.Logf("Submitted %v events to SQ\n", ret)

	ch := make(chan cqe)
	go r.ListenCQ(ch)

	cqe := <-ch
	t.Logf("Write %v bytes to file\n", cqe.res)
}
