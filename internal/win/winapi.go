// Package win wraps the small subset of the Win32 API the daemon needs:
// DPI awareness, cursor state and injected mouse input.
package win

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	winmm    = syscall.NewLazyDLL("winmm.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSendInput                     = user32.NewProc("SendInput")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetClassNameW                 = user32.NewProc("GetClassNameW")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procEnumWindows                   = user32.NewProc("EnumWindows")
	procIsWindowVisible               = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	procGetCurrentThreadId            = kernel32.NewProc("GetCurrentThreadId")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procAttachThreadInput             = user32.NewProc("AttachThreadInput")
	procBringWindowToTop              = user32.NewProc("BringWindowToTop")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procIsIconic                      = user32.NewProc("IsIconic")
	procTimeBeginPeriod               = winmm.NewProc("timeBeginPeriod")
	procTimeEndPeriod                 = winmm.NewProc("timeEndPeriod")
	procGetCurrentProcess             = kernel32.NewProc("GetCurrentProcess")
)

const (
	inputMouse = 0

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79
)

type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	DwFlags   uint32
	Time      uint32
	ExtraInfo uintptr
}

type input struct {
	Type uint32
	Mi   mouseInput
}

type point struct {
	X, Y int32
}

type rect struct {
	Left, Top, Right, Bottom int32
}

// dpiAwarenessContextPerMonitorAwareV2 is DPI_AWARENESS_CONTEXT(-4).
const dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

// EnablePerMonitorDpiV2 makes GetCursorPos / SendInput operate in true
// physical pixels regardless of the display scale factor. Must be called
// before any cursor API is used.
func EnablePerMonitorDpiV2() {
	r1, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
	if r1 == 0 {
		// Fallback for older Windows (>= Vista): system-DPI aware.
		procSetProcessDPIAware.Call()
	}
}

// SystemDpi returns the system DPI (96 = 100% scaling).
func SystemDpi() int {
	r, _, _ := procGetDpiForSystem.Call()
	if r == 0 {
		return 96
	}
	return int(r)
}

func getSystemMetrics(index int) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(r)
}

// VirtualDesktop returns the bounding box of the entire virtual desktop in
// physical pixels: origin X, origin Y, width, height.
func VirtualDesktop() (int, int, int, int) {
	x := getSystemMetrics(smXVirtualScreen)
	y := getSystemMetrics(smYVirtualScreen)
	w := getSystemMetrics(smCxVirtualScreen)
	h := getSystemMetrics(smCyVirtualScreen)
	return int(x), int(y), int(w), int(h)
}

// CursorPos returns the current cursor position in physical pixels.
func CursorPos() (int, int) {
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return int(pt.X), int(pt.Y)
}

func sendMouse(flags uint32, dx, dy int32, mouseData uint32) error {
	in := input{Type: inputMouse}
	in.Mi = mouseInput{Dx: dx, Dy: dy, MouseData: mouseData, DwFlags: flags}
	r1, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if r1 == 0 {
		return fmt.Errorf("SendInput failed: %v", err)
	}
	return nil
}

// toAbsolute converts physical desktop pixels to the normalized
// 0..65535 space SendInput expects with MOUSEEVENTF_VIRTUALDESK.
func toAbsolute(x, y int) (int32, int32) {
	vx, vy, vw, vh := VirtualDesktop()
	if vw <= 0 {
		vw = 1
	}
	if vh <= 0 {
		vh = 1
	}
	nx := int32((int64(x-vx) * 65535) / int64(vw-1))
	ny := int32((int64(y-vy) * 65535) / int64(vh-1))
	return nx, ny
}

// MoveToAbsolute teleports the cursor to a physical desktop coordinate.
// Used only as the final "land exactly on target" fixup; real movement is
// done by the trajectory engine in package mouse.
func MoveToAbsolute(x, y int) error {
	nx, ny := toAbsolute(x, y)
	return sendMouse(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny, 0)
}

func pressRelease(down, up uint32) error {
	if err := sendMouse(down, 0, 0, 0); err != nil {
		return err
	}
	return sendMouse(up, 0, 0, 0)
}

// ClickLeft injects a left button press+release at the current position.
func ClickLeft() error { return pressRelease(mouseeventfLeftDown, mouseeventfLeftUp) }

// ClickRight injects a right button press+release at the current position.
func ClickRight() error { return pressRelease(mouseeventfRightDown, mouseeventfRightUp) }

// ClickMiddle injects a middle button press+release at the current position.
func ClickMiddle() error { return pressRelease(mouseeventfMiddleDown, mouseeventfMiddleUp) }

// ButtonDown / ButtonUp hold and release a button ("left", "right", "middle").
func ButtonDown(btn string) error {
	switch btn {
	case "right":
		return sendMouse(mouseeventfRightDown, 0, 0, 0)
	case "middle":
		return sendMouse(mouseeventfMiddleDown, 0, 0, 0)
	default:
		return sendMouse(mouseeventfLeftDown, 0, 0, 0)
	}
}

func ButtonUp(btn string) error {
	switch btn {
	case "right":
		return sendMouse(mouseeventfRightUp, 0, 0, 0)
	case "middle":
		return sendMouse(mouseeventfMiddleUp, 0, 0, 0)
	default:
		return sendMouse(mouseeventfLeftUp, 0, 0, 0)
	}
}

// Wheel scrolls by notches (positive = up).
func Wheel(notches int) error {
	return sendMouse(mouseeventfWheel, 0, 0, uint32(int32(notches*120)))
}

// TimeBeginPeriod raises the timer resolution to ~1ms so trajectory
// frames are paced smoothly.
func TimeBeginPeriod() { procTimeBeginPeriod.Call(1) }

// TimeEndPeriod restores the default timer resolution.
func TimeEndPeriod() { procTimeEndPeriod.Call(1) }

// ForegroundWindow returns title and class of the current foreground window.
func ForegroundWindow() (title, class string) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", ""
	}
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title = syscall.UTF16ToString(buf[:n])
	cls := make([]uint16, 256)
	n, _, _ = procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
	class = syscall.UTF16ToString(cls[:n])
	return title, class
}

// ForegroundRect returns the bounding rect of the foreground window.
func ForegroundRect() (int, int, int, int, bool) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0, 0, 0, 0, false
	}
	var r rect
	r1, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if r1 == 0 {
		return 0, 0, 0, 0, false
	}
	return int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top), true
}

var chromeWindowClass = syscall.StringToUTF16Ptr("Chrome_WidgetWin_1")

func isChromeWnd(hwnd uintptr) bool {
	cls := make([]uint16, 64)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
	return syscall.UTF16ToString(cls[:n]) == "Chrome_WidgetWin_1"
}

func windowRect(hwnd uintptr) (int, int, int, int, bool) {
	var r rect
	r1, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if r1 == 0 {
		return 0, 0, 0, 0, false
	}
	return int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top), true
}

// FindChromeWindowNear returns the handle of a visible Chrome-class window
// for the given tab: windows are matched by rect proximity, preferring ones
// whose title contains tabTitle when several windows share the same rect
// (overlapping browser windows are common on real desktops).
func FindChromeWindowNear(x, y, w, h, tol int, tabTitle string) uintptr {
	var fallback, best uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if best != 0 {
			return 1
		}
		if !isChromeWnd(hwnd) {
			return 1
		}
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		titleMatch := false
		if tabTitle != "" {
			buf := make([]uint16, 512)
			n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			titleMatch = strings.Contains(syscall.UTF16ToString(buf[:n]), tabTitle)
		}
		rectOk := false
		if w == 0 && h == 0 {
			rectOk = true
		} else if wx, wy, ww, wh, ok := windowRect(hwnd); ok {
			rectOk = abs(wx-x) <= tol && abs(wy-y) <= tol && abs(ww-w) <= tol && abs(wh-h) <= tol
		}
		if rectOk {
			if titleMatch {
				best = hwnd
				return 0
			}
			if fallback == 0 {
				fallback = hwnd
			}
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if best != 0 {
		return best
	}
	return fallback
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// IsForeground reports whether hwnd is the current foreground window.
func IsForeground(hwnd uintptr) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	return fg != 0 && fg == hwnd
}

// SetForegroundReliably brings hwnd to the foreground, working around the
// Windows foreground lock via AttachThreadInput. Returns actual success.
func SetForegroundReliably(hwnd uintptr) bool {
	if icon, _, _ := procIsIconic.Call(hwnd); icon != 0 {
		procShowWindow.Call(hwnd, 9 /*SW_RESTORE*/)
		time.Sleep(300 * time.Millisecond)
	}
	if IsForeground(hwnd) {
		return true
	}
	fg, _, _ := procGetForegroundWindow.Call()
	thisThread, _, _ := procGetCurrentThreadId.Call()
	attached := false
	if fg != 0 {
		fgThread, _, _ := procGetWindowThreadProcessId.Call(fg, 0)
		if fgThread != 0 && fgThread != thisThread {
			r, _, _ := procAttachThreadInput.Call(thisThread, fgThread, 1)
			attached = r != 0
		}
	}
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	if attached {
		procAttachThreadInput.Call(thisThread, 0, 0)
	}
	time.Sleep(120 * time.Millisecond)
	if IsForeground(hwnd) {
		return true
	}
	// Second chance: bring to top and retry (helps when no fg thread).
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	time.Sleep(120 * time.Millisecond)
	return IsForeground(hwnd)
}
