package main

// Interactive flows: record real hand trajectories, replay stored ones,
// and a self-test that drives the cursor with the engine itself.

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mousebridge/internal/mouse"
	"mousebridge/internal/trajlab"
	"mousebridge/internal/win"
)

var stdin *bufio.Scanner

func readLine() (string, bool) {
	if stdin == nil {
		stdin = bufio.NewScanner(os.Stdin)
	}
	if !stdin.Scan() {
		return "", false // EOF / no tty: proceed without blocking
	}
	return strings.TrimSpace(stdin.Text()), true
}

func waitEnter(prompt string) {
	if _, ok := readLine(); !ok {
		fmt.Println("  （无终端输入，自动继续）")
		return
	}
	_ = prompt
}

// computeRange finds a start/end pair for a movement of the given length
// and direction, centered on the virtual desktop, keeping a margin. It
// reports the longest movement that fits in this direction.
func computeRange(dist, angle float64) (start, end [2]float64, avail float64, ok bool) {
	vx, vy, vw, vh := win.VirtualDesktop()
	const margin = 70
	rad := angle * math.Pi / 180
	ax, ay := math.Abs(math.Cos(rad)), math.Abs(math.Sin(rad))
	spanX, spanY := math.Inf(1), math.Inf(1)
	if ax > 1e-6 {
		spanX = float64(vw-2*margin) / ax
	}
	if ay > 1e-6 {
		spanY = float64(vh-2*margin) / ay
	}
	avail = math.Min(spanX, spanY)
	if dist > avail {
		return start, end, avail, false
	}
	cx, cy := float64(vx)+float64(vw)/2, float64(vy)+float64(vh)/2
	start = [2]float64{cx - dist/2*math.Cos(rad), cy - dist/2*math.Sin(rad)}
	end = [2]float64{cx + dist/2*math.Cos(rad), cy + dist/2*math.Sin(rad)}
	return start, end, avail, true
}

func placeCursorAt(p [2]float64) bool {
	if err := win.MoveToAbsolute(int(math.Round(p[0])), int(math.Round(p[1]))); err != nil {
		return false
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		x, y := win.CursorPos()
		if math.Abs(float64(x)-p[0]) <= 2 && math.Abs(float64(y)-p[1]) <= 2 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func pollCursor(rec *trajlab.Recorder, done chan<- struct{}) {
	win.TimeBeginPeriod()
	defer win.TimeEndPeriod()
	t0 := time.Now()
	for {
		x, y := win.CursorPos()
		rec.Feed(float64(x), float64(y), float64(time.Since(t0).Microseconds())/1000)
		if s := rec.State(); s == "done" || s == "timeout" {
			close(done)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------- record ----------

func cmdRecord(args []string) {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	dists := fs.String("dists", "500", "逗号分隔的距离列表 (px)")
	angles := fs.String("angles", "0", "逗号分隔的方向列表 (度)")
	count := fs.Int("count", 1, "每个 (距离,方向) 组合录几条")
	dir := fs.String("dir", trajlab.DefaultDir(), "轨迹库目录")
	fs.Parse(args)

	ds, as := parseFloats(*dists), parseFloats(*angles)
	if len(ds) == 0 || len(as) == 0 || *count < 1 {
		fmt.Println("距离/方向/次数不能为空")
		os.Exit(2)
	}
	ov := NewOverlay()
	defer ov.Close()
	ov.Show()

	total := len(ds) * len(as) * *count
	n := 0
	failed := 0
	for _, d := range ds {
		for _, a := range as {
			for k := 1; k <= *count; k++ {
				n++
				title := fmt.Sprintf("录制 %d/%d · %.0fpx @ %.0f° (%s)", n, total, d, a, angleName(a))
				if ok := runTake(ov, title, d, a, *dir); !ok {
					failed++
				}
			}
		}
	}
	ov.Set("录制结束", fmt.Sprintf("成功 %d 条，失败 %d 条", total-failed, failed), nil, nil, nil)
	time.Sleep(800 * time.Millisecond)
	fmt.Printf("完成：%s 共 %d 条（成功 %d）\n", *dir, total, total-failed)
}

func angleName(a float64) string {
	n := int(math.Mod(math.Mod(a, 360)+360, 360)/45+0.5) % 8
	switch n {
	case 0:
		return "向右"
	case 1:
		return "右下"
	case 2:
		return "向下"
	case 3:
		return "左下"
	case 4:
		return "向左"
	case 5:
		return "左上"
	case 6:
		return "向上"
	default:
		return "右上"
	}
}

func runTake(ov *Overlay, title string, dist, angle float64, dir string) bool {
	start, end, avail, ok := computeRange(dist, angle)
	if !ok {
		fmt.Printf("  ✘ %s：屏幕放不下 %.0fpx（该方向最大约 %.0fpx），已跳过\n", title, dist, avail)
		return false
	}
	lines := []string{
		fmt.Sprintf("目标距离 %.0fpx · 方向 %s · 起点绿圈 → 终点红圈", dist, angleName(angle)),
		"听到提示音后，用自然的一次移动从绿圈移到红圈",
	}
	ov.Set(title, "光标就位中…", lines, &start, &end)
	if !placeCursorAt(start) {
		fmt.Printf("  ✘ %s：光标未能就位\n", title)
		return false
	}
	ov.SetState("就位 — 按 Enter（提示音后立刻移动）")
	fmt.Printf("%s\n  起点 (%.0f,%.0f) 终点 (%.0f,%.0f)，按 Enter 开始…\n", title, start[0], start[1], end[0], end[1])
	waitEnter("")
	beep(880, 150)
	ov.SetState("开始移动！")

	rec := trajlab.NewRecorder(trajlab.DefaultRecCfg())
	done := make(chan struct{})
	go pollCursor(rec, done)
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
reporting:
	for {
		select {
		case <-done:
			break reporting
		case <-ticker.C:
			switch rec.State() {
			case "wait":
				ov.SetState("等待起手…")
			case "move", "rest":
				ov.SetState("记录中…")
			}
		}
	}
	if rec.TimedOut() {
		ov.SetState("超时 — 本条作废")
		fmt.Println("  ✘ 超时：未检测到移动或移动未停止")
		return false
	}
	trim := rec.Trimmed()
	if len(trim) < 5 {
		ov.SetState("数据太短 — 本条作废")
		fmt.Println("  ✘ 有效样本太少")
		return false
	}
	m := trajlab.ComputeMetrics(trim, &end)
	_, _, sw, sh := win.VirtualDesktop()
	tf := &trajlab.TrajFile{
		Kind:       "human",
		Dist:       m.Chord,
		AngleDeg:   trajlab.AngleOf(trim),
		Start:      [2]float64{trim[0].X, trim[0].Y},
		End:        [2]float64{trim[len(trim)-1].X, trim[len(trim)-1].Y},
		ScreenW:    sw,
		ScreenH:    sh,
		DurationMs: m.DurationMs,
		PeakSpeed:  m.PeakSpeed,
		ArcChord:   m.ArcChord,
		Points:     trim,
	}
	p, err := trajlab.Save(dir, tf)
	if err != nil {
		ov.SetState("保存失败")
		fmt.Println("  ✘ 保存失败:", err)
		return false
	}
	ov.SetState("已保存 " + filepath.Base(p))
	beep(1320, 120)
	fmt.Printf("  ✔ %s  实际 %.0fpx@%.0f°  用时 %.0fms  峰速 %.0fpx/s  弧/弦 %.3f  终点偏差 %.1fpx\n",
		filepath.Base(p), m.Chord, tf.AngleDeg, m.DurationMs, m.PeakSpeed, m.ArcChord, m.EndErr)
	if m.EndErr > math.Max(12, dist*0.03) {
		fmt.Println("    ⚠ 没有停在红圈上，建议删掉这条重录")
	}
	return true
}

// ---------- replay ----------

func cmdReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	file := fs.String("file", "", "轨迹 JSON 文件")
	dist := fs.Float64("dist", 0, "按距离/方向从库里挑（-file 的替代）")
	angle := fs.Float64("angle", 0, "方向（配合 -dist）")
	tol := fs.Float64("tol", 30, "挑选容差 (px)")
	dir := fs.String("dir", trajlab.DefaultDir(), "轨迹库目录")
	toDist := fs.Float64("to-dist", -1, "重标定的距离 (默认=原距离)")
	toAngle := fs.Float64("to-angle", -1, "重标定的方向 (默认=原方向)")
	startFlag := fs.String("start", "", "新起点 x,y（默认屏幕居中放置）")
	fs.Parse(args)

	var traj *trajlab.TrajFile
	if *file != "" {
		t, err := loadTrajFile(*file)
		if err != nil {
			fmt.Println("读取失败:", err)
			os.Exit(1)
		}
		traj = t
	} else {
		files, err := trajlab.LoadAll(*dir)
		if err != nil {
			fmt.Println("读取轨迹库失败:", err)
			os.Exit(1)
		}
		got := trajlab.Pick(files, *dist, *angle, *tol)
		if got.Traj == nil {
			fmt.Println("库里没有匹配的真人轨迹:", got.Reason)
			os.Exit(1)
		}
		fmt.Printf("选中 %s（置信度 %.2f，%s）\n", baseName(got.File), got.Confidence, got.Reason)
		traj = got.Traj
	}

	td, ta := traj.Dist, traj.AngleDeg
	if *toDist > 0 {
		td = *toDist
	}
	if *toAngle >= 0 {
		ta = *toAngle
	}
	var start [2]float64
	if *startFlag != "" {
		parts := strings.Split(*startFlag, ",")
		if len(parts) != 2 {
			fmt.Println("-start 格式应为 x,y")
			os.Exit(2)
		}
		x, _ := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		y, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		start = [2]float64{x, y}
	} else {
		s, _, _, ok := computeRange(td, ta)
		if !ok {
			fmt.Println("屏幕放不下这个几何")
			os.Exit(1)
		}
		start = s
	}
	pts := trajlab.Rescale(traj, start, td, ta)
	last := pts[len(pts)-1]

	ov := NewOverlay()
	defer ov.Close()
	title := fmt.Sprintf("回放 %s → %.0fpx @ %.0f°", baseName(traj.File), td, ta)
	ov.Set(title, "3 秒后开始", []string{
		fmt.Sprintf("原始: %.0fpx@%.0f° · %d 个采样点", traj.Dist, traj.AngleDeg, len(pts)),
	}, &start, &[2]float64{last.X, last.Y})
	ov.Show()
	for i := 3; i >= 1; i-- {
		ov.SetState(fmt.Sprintf("%d…", i))
		beep(660, 90)
		time.Sleep(700 * time.Millisecond)
	}
	beep(990, 150)
	ov.SetState("回放中…")

	if err := win.MoveToAbsolute(int(math.Round(pts[0].X)), int(math.Round(pts[0].Y))); err != nil {
		fmt.Println("就位失败:", err)
		os.Exit(1)
	}
	t0 := time.Now()
	for _, p := range pts {
		deadline := t0.Add(time.Duration(p.T * float64(time.Millisecond)))
		for {
			d := time.Until(deadline)
			if d <= 0 {
				break
			}
			if d > 2*time.Millisecond {
				time.Sleep(2 * time.Millisecond)
			} else {
				time.Sleep(d)
			}
		}
		win.MoveToAbsolute(int(math.Round(p.X)), int(math.Round(p.Y)))
	}
	win.MoveToAbsolute(int(math.Round(last.X)), int(math.Round(last.Y)))
	fmt.Printf("回放完成：%.0fpx@%.0f°，用时 %.0fms\n", td, ta, pts[len(pts)-1].T)
	ov.SetState("回放完成")
	time.Sleep(500 * time.Millisecond)
}

func loadTrajFile(p string) (*trajlab.TrajFile, error) {
	files, err := trajlab.LoadAll(filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.File == p || baseName(f.File) == filepath.Base(p) {
			return f, nil
		}
	}
	return nil, errors.New("找不到该轨迹文件")
}

// ---------- selftest ----------

// cmdSelftest drives the cursor with the engine itself along a known path
// and checks the whole pipeline: polling → motion detection → trimming →
// metrics → store. MOVES THE REAL CURSOR for about a second.
func cmdSelftest(args []string) {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	dir := fs.String("dir", trajlab.DefaultDir(), "轨迹库目录")
	fs.Parse(args)

	A := [2]float64{360, 260}
	B := [2]float64{880, 410} // 520px right, 150px down
	fmt.Println("自检：引擎将驱动光标从", A, "到", B, "…")
	win.MoveToAbsolute(int(A[0]), int(A[1]))
	time.Sleep(120 * time.Millisecond)

	rec := trajlab.NewRecorder(trajlab.DefaultRecCfg())
	done := make(chan struct{})
	go pollCursor(rec, done)
	time.Sleep(120 * time.Millisecond) // 静止段，应被裁掉
	mouse.Move(B[0], B[1])
	<-done

	fail := func(msg string) {
		fmt.Println("✘ 自检失败:", msg)
		os.Exit(1)
	}
	if rec.TimedOut() {
		fail("录制超时（期间鼠标被占用了？）")
	}
	trim := rec.Trimmed()
	if len(trim) < 10 {
		fail(fmt.Sprintf("有效样本太少 (%d)", len(trim)))
	}
	tgt := [2]float64{B[0], B[1]}
	m := trajlab.ComputeMetrics(trim, &tgt)
	chord := math.Hypot(B[0]-A[0], B[1]-A[1])
	_, _, sw, sh := win.VirtualDesktop()
	checks := []struct {
		name string
		ok   bool
		val  string
	}{
		{"弦长≈541px±25", math.Abs(m.Chord-chord) <= 25, fmt.Sprintf("%.1f", m.Chord)},
		{"终点偏差≤3px", m.EndErr <= 3, fmt.Sprintf("%.2fpx", m.EndErr)},
		{"时长≥150ms", m.DurationMs >= 150, fmt.Sprintf("%.0fms", m.DurationMs)},
		{"检出段数≥1", len(m.Segments) >= 1, fmt.Sprintf("%d", len(m.Segments))},
		{"无跳变≤15000px/s", m.MaxJumpPxS <= 15000, fmt.Sprintf("%.0fpx/s", m.MaxJumpPxS)},
		{"静止段被裁掉", trim[0].T <= 30, fmt.Sprintf("首样本t=%.0fms", trim[0].T)},
	}
	tf := &trajlab.TrajFile{
		Kind: "synthetic", Dist: m.Chord, AngleDeg: trajlab.AngleOf(trim),
		Start: [2]float64{trim[0].X, trim[0].Y}, End: [2]float64{trim[len(trim)-1].X, trim[len(trim)-1].Y},
		ScreenW: sw, ScreenH: sh,
		DurationMs: m.DurationMs, PeakSpeed: m.PeakSpeed, ArcChord: m.ArcChord,
		Points: trim, Notes: "trajlab selftest (engine-driven)",
	}
	p, err := trajlab.Save(*dir, tf)
	if err != nil {
		fail("保存失败: " + err.Error())
	}
	passed := 0
	for _, c := range checks {
		mark := "✘"
		if c.ok {
			mark = "✔"
			passed++
		}
		fmt.Printf("  %s %-16s %s\n", mark, c.name, c.val)
	}
	fmt.Printf("自检结果: %d/%d 通过 · 已存 %s\n", passed, len(checks), filepath.Base(p))
	if passed != len(checks) {
		os.Exit(1)
	}
}
