package main

// Full-screen topmost overlay for the recording flow. A colorkey-layered
// window: magenta pixels are transparent, so only the drawn text and the
// start/end markers are visible; WS_EX_TRANSPARENT makes it click-through.
// Pure Win32/GDI via syscall — no GUI dependencies.

import (
	"runtime"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"mousebridge/internal/win"
)

var (
	user32Ov = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW = user32Ov.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32Ov.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32Ov.NewProc("DefWindowProcW")
	procGetMessageW      = user32Ov.NewProc("GetMessageW")
	procTranslateMessage = user32Ov.NewProc("TranslateMessage")
	procDispatchMessageW = user32Ov.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32Ov.NewProc("PostQuitMessage")
	procShowWindowOv     = user32Ov.NewProc("ShowWindow")
	procInvalidateRect   = user32Ov.NewProc("InvalidateRect")
	procUpdateWindowOv   = user32Ov.NewProc("UpdateWindow")
	procDestroyWindow    = user32Ov.NewProc("DestroyWindow")
	procSetLayeredAttrs  = user32Ov.NewProc("SetLayeredWindowAttributes")
	procGetDC            = user32Ov.NewProc("GetDC")
	procReleaseDC        = user32Ov.NewProc("ReleaseDC")
	procLoadCursorOv     = user32Ov.NewProc("LoadCursorW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procBeep             = kernel32.NewProc("Beep")

	procCreateFontW    = gdi32.NewProc("CreateFontW")
	procSelectObject   = gdi32.NewProc("SelectObject")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
	procCreateBrush    = gdi32.NewProc("CreateSolidBrush")
	procCreatePen      = gdi32.NewProc("CreatePen")
	procGetStockObject = gdi32.NewProc("GetStockObject")
	procFillRect       = user32Ov.NewProc("FillRect")
	procEllipse        = gdi32.NewProc("Ellipse")
	procSetTextColor   = gdi32.NewProc("SetTextColor")
	procSetBkMode      = gdi32.NewProc("SetBkMode")
	procSetTextAlign   = gdi32.NewProc("SetTextAlign")
	procTextOutW       = gdi32.NewProc("TextOutW")
	procCompatDC       = gdi32.NewProc("CreateCompatibleDC")
	procCompatBitmap   = gdi32.NewProc("CreateCompatibleBitmap")
	procBitBlt         = gdi32.NewProc("BitBlt")
	procDeleteDC       = gdi32.NewProc("DeleteDC")
)

const (
	wsPopup         = 0x80000000
	wsVisible       = 0x10000000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExNoActivate  = 0x08000000

	wmPaint   = 0x000F
	wmDestroy = 0x0002

	lwaColorKey = 0x00000001
	colorKey    = 0x00FF00FF // magenta = fully transparent

	taTopCenter   = 0x0006 // TA_CENTER | TA_TOP
	bkTransparent = 1
	psSolid       = 0
	hollowBrush   = 5
	srccopy       = 0x00CC0020
	swHide        = 0
	swShowNoAct   = 4
	idcArrow      = 32512

	colWhite  uint32 = 0x00FFFFFF
	colYellow uint32 = 0x0000FFFF
	colGreen  uint32 = 0x005AE600 // rgb(0,230,90)
	colRed    uint32 = 0x004040FF // rgb(255,64,64)
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type msg struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	_       [4]byte // tail padding to MSG's 48 bytes
}

type rect struct{ Left, Top, Right, Bottom int32 }

var wndProcPtr = syscall.NewCallback(func(hwnd, msgp, wp, lp uintptr) uintptr {
	switch msgp {
	case wmPaint:
		paintOverlay()
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msgp, wp, lp)
	return r
})

// Overlay shows the recording instructions and the start/end markers.
type Overlay struct {
	mu     sync.Mutex
	hwnd   uintptr
	w, h   int
	vx, vy int // virtual-desktop origin for marker translation

	title, state string
	lines        []string
	startPt      [2]float64
	endPt        [2]float64
	showMarkers  bool

	ready chan struct{}
	fonts []uintptr
}

var theOverlay *Overlay

func NewOverlay() *Overlay {
	o := &Overlay{ready: make(chan struct{})}
	theOverlay = o
	go o.run()
	<-o.ready
	return o
}

func (o *Overlay) run() {
	runtime.LockOSThread()
	hInst, _, _ := procGetModuleHandleW.Call(0)
	cn, _ := syscall.UTF16PtrFromString("MBTrajOverlay")
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProcPtr,
		HInstance:     syscall.Handle(hInst),
		LpszClassName: cn,
	}
	hc, _, _ := procLoadCursorOv.Call(0, uintptr(idcArrow))
	wc.HCursor = syscall.Handle(hc)
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	vx, vy, vw, vh := win.VirtualDesktop()
	up := func(v int) uintptr { return uintptr(v) }
	hwnd, _, _ := procCreateWindowExW.Call(
		up(wsExTopmost|wsExLayered|wsExTransparent|wsExToolWindow|wsExNoActivate),
		uintptr(unsafe.Pointer(cn)), uintptr(unsafe.Pointer(cn)),
		up(wsPopup|wsVisible),
		up(vx), up(vy), up(vw), up(vh),
		0, 0, hInst, 0)
	o.hwnd = hwnd
	o.w, o.h = vw, vh
	o.vx, o.vy = vx, vy
	o.fonts = []uintptr{
		makeFont(-64, true),  // title
		makeFont(-46, true),  // state
		makeFont(-30, false), // lines
	}
	procSetLayeredAttrs.Call(hwnd, uintptr(colorKey), 0, uintptr(lwaColorKey))
	procShowWindowOv.Call(hwnd, uintptr(swHide))
	close(o.ready)

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// Set updates all overlay content in one shot; nil marker hides the rings.
func (o *Overlay) Set(title, state string, lines []string, start, end *[2]float64) {
	o.mu.Lock()
	o.title, o.state, o.lines = title, state, lines
	o.showMarkers = start != nil
	if start != nil {
		o.startPt = *start
	}
	if end != nil {
		o.endPt = *end
	}
	o.mu.Unlock()
	o.repaint()
}

// SetState updates just the status line (safe from any goroutine).
func (o *Overlay) SetState(state string) {
	o.mu.Lock()
	o.state = state
	o.mu.Unlock()
	o.repaint()
}

func (o *Overlay) Show() {
	if o.hwnd != 0 {
		procShowWindowOv.Call(o.hwnd, uintptr(swShowNoAct))
		o.repaint()
	}
}

func (o *Overlay) Hide() {
	if o.hwnd != 0 {
		procShowWindowOv.Call(o.hwnd, uintptr(swHide))
	}
}

func (o *Overlay) repaint() {
	if o.hwnd != 0 {
		procInvalidateRect.Call(o.hwnd, 0, 0)
		procUpdateWindowOv.Call(o.hwnd)
	}
}

func (o *Overlay) Close() {
	o.Hide()
}

func paintOverlay() {
	o := theOverlay
	if o == nil || o.hwnd == 0 {
		return
	}
	o.mu.Lock()
	title, state := o.title, o.state
	lines := append([]string(nil), o.lines...)
	sp, ep, showM := o.startPt, o.endPt, o.showMarkers
	hwnd, w, h := o.hwnd, o.w, o.h
	o.mu.Unlock()

	dc, _, _ := procGetDC.Call(hwnd)
	mem, _, _ := procCompatDC.Call(dc)
	bmp, _, _ := procCompatBitmap.Call(dc, uintptr(w), uintptr(h))
	oldBmp, _, _ := procSelectObject.Call(mem, bmp)

	// colorkey background
	br, _, _ := procCreateBrush.Call(uintptr(colorKey))
	rc := rect{0, 0, int32(w), int32(h)}
	procFillRect.Call(mem, uintptr(unsafe.Pointer(&rc)), br)
	procDeleteObject.Call(br)
	procSetBkMode.Call(mem, bkTransparent)

	drawCentered(mem, title, w/2, int(float64(h)*0.05), o.fonts[0], colWhite)
	drawCentered(mem, state, w/2, int(float64(h)*0.14), o.fonts[1], colYellow)
	for i, ln := range lines {
		drawCentered(mem, ln, w/2, int(float64(h)*0.23)+i*42, o.fonts[2], colWhite)
	}
	if showM {
		ring(mem, int(sp[0])-o.vx, int(sp[1])-o.vy, colGreen)
		ring(mem, int(ep[0])-o.vx, int(ep[1])-o.vy, colRed)
	}

	procBitBlt.Call(dc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, uintptr(srccopy))
	procSelectObject.Call(mem, oldBmp)
	procDeleteObject.Call(bmp)
	procDeleteDC.Call(mem)
	procReleaseDC.Call(hwnd, dc)
}

func drawCentered(dc uintptr, s string, x, y int, font uintptr, col uint32) {
	if s == "" {
		return
	}
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return
	}
	oldF, _, _ := procSelectObject.Call(dc, font)
	procSetTextColor.Call(dc, uintptr(col))
	procSetTextAlign.Call(dc, uintptr(taTopCenter))
	procTextOutW.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)))
	procSelectObject.Call(dc, oldF)
}

func ring(dc uintptr, x, y int, col uint32) {
	if x < 40 || y < 40 {
		return
	}
	const r = 26
	pen, _, _ := procCreatePen.Call(psSolid, 5, uintptr(col))
	oldPen, _, _ := procSelectObject.Call(dc, pen)
	oldBr, _, _ := procSelectObject.Call(dc, uintptr(hollowBrush))
	procEllipse.Call(dc, uintptr(x-r), uintptr(y-r), uintptr(x+r), uintptr(y+r))
	procSelectObject.Call(dc, oldPen)
	procSelectObject.Call(dc, oldBr)
	procDeleteObject.Call(pen)

	// center dot
	dot, _, _ := procCreateBrush.Call(uintptr(col))
	oldDot, _, _ := procSelectObject.Call(dc, dot)
	procEllipse.Call(dc, uintptr(x-4), uintptr(y-4), uintptr(x+4), uintptr(y+4))
	procSelectObject.Call(dc, oldDot)
	procDeleteObject.Call(dot)
}

func makeFont(h int, bold bool) uintptr {
	weight := uintptr(400)
	if bold {
		weight = 700
	}
	face, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	const nonAntialiasedQuality = 5 // keeps edges away from the colorkey
	r, _, _ := procCreateFontW.Call(
		uintptr(uint32(h)), 0, 0, 0, weight,
		0, 0, 0, 0, 0, 0,
		uintptr(nonAntialiasedQuality), 0, uintptr(unsafe.Pointer(face)))
	return r
}

func beep(freq, ms int) {
	procBeep.Call(uintptr(freq), uintptr(ms))
}
