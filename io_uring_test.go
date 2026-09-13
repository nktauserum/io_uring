package io_uring

import (
	"os"
	"testing"
	"unsafe"
)

var wanted = "Hello World!"

func TestSetup(t *testing.T) {
	var r ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fail()
	}
}

func TestWriteToFile(t *testing.T) {
	var r ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fatalf("Error setup io_uring: code %v\n", errno)
	}

	f, err := os.Create("./example.txt")
	if err != nil {
		t.Fatalf("error creating file: %v\n", err)
	}
	defer f.Close()

	txt := []byte(wanted)

	submit_to_sq(&r, 23, int32(f.Fd()), uintptr(unsafe.Pointer(&txt[0])), uint32(len(txt)), 0)

	res, ok := read_from_cq(&r)
	if !ok {
		t.Fail()
	}
	t.Logf("Write %v bytes to file\n", res)
}

func TestReadFromFile(t *testing.T) {
	var r ring
	errno := setup(2, &r, 0)
	if errno != 0 {
		t.Fail()
	}

	f, err := os.Open("./example.txt")
	if err != nil {
		t.Fatalf("error os.Open(): %v\n", err)
	}
	defer f.Close()
	defer os.Remove("./example.txt")

	buf := make([]byte, 1024)
	submit_to_sq(&r, 22, int32(f.Fd()), uintptr(unsafe.Pointer(&buf[0])), 1024, 0)

	if string(buf[:len(wanted)]) != wanted {
		t.Fatalf("Error: got %v, wanted %v\n", string(buf), wanted)
	}
}
