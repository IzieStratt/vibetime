package presence

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getLastInputInfo = user32.NewProc("GetLastInputInfo")
	getTickCount     = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

func systemIdle() (time.Duration, error) {
	li := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r, _, err := getLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0, err
	}
	now, _, _ := getTickCount.Call()
	return time.Duration(uint32(now)-li.dwTime) * time.Millisecond, nil // uint32 wraparound safe
}
