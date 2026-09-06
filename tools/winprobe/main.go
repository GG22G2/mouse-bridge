package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"mousebridge/internal/win"
)

func main() {
	win.EnablePerMonitorDpiV2()
	fmt.Println("system dpi:", win.SystemDpi())
	vx, vy, vw, vh := win.VirtualDesktop()
	fmt.Printf("virtual desktop: (%d,%d) %dx%d\n", vx, vy, vw, vh)

	user32 := syscall.NewLazyDLL("user32.dll")
	procEnumWindows := user32.NewProc("EnumWindows")
	procGetClassNameW := user32.NewProc("GetClassNameW")
	procIsWindowVisible := user32.NewProc("IsWindowVisible")
	procGetWindowTextW := user32.NewProc("GetWindowTextW")
	procGetWindowRect := user32.NewProc("GetWindowRect")

	type rect struct{ L, T, R, B int32 }
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		cls := make([]uint16, 64)
		n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), 64)
		class := syscall.UTF16ToString(cls[:n])
		if class != "Chrome_WidgetWin_1" {
			return 1
		}
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		var r rect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		buf := make([]uint16, 256)
		n2, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
		fmt.Printf("hwnd=%x class=%s rect=(%d,%d)-(%d,%d) size=%dx%d title=%q\n",
			hwnd, class, r.L, r.T, r.R, r.B, r.R-r.L, r.B-r.T, syscall.UTF16ToString(buf[:n2]))
		return 1
	})
	procEnumWindows.Call(cb, 0)
}
