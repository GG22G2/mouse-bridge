// trajlab: browser-free test bench for the mouse trajectory engine.
//
// Commands:
//
//	analyze   run the acceptance-gate matrix over distance × direction
//	          (pure compute, never touches the real cursor)
//	record    record REAL hand movements behind a screen overlay; the
//	          cursor is placed at the start point, motion start/end are
//	          detected automatically, static lead-in/tail are trimmed,
//	          and each take is saved as its own JSON file
//	list      show the trajectory library
//	pick      choose recorded-vs-synthetic for a wanted distance and
//	          direction by confidence
//	replay    play a stored (human) trajectory back, rescaled onto new
//	          geometry
//	selftest  drive the cursor with the engine itself and check the
//	          recorder end-to-end (moves the real cursor!)
//
// Trajectory library: %USERPROFILE%\.mouse-bridge\trajectories (override
// with MOUSEBRIDGE_TRAJ_DIR or -dir). One JSON file per trajectory, named
// d<dist>_a<angle>_<seq>.json — copy the folder to another machine as-is.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"mousebridge/internal/trajlab"
	"mousebridge/internal/win"
)

func main() {
	win.EnablePerMonitorDpiV2()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "analyze":
		cmdAnalyze(os.Args[2:])
	case "record":
		cmdRecord(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "pick":
		cmdPick(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	case "selftest":
		cmdSelftest(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`用法: trajlab <command> [flags]

  analyze   轨迹生成算法验收矩阵（纯计算，不碰真实光标）
            -reps 16  -distances 10,50,...  -angles 0,45,...  -out report.json
  record    录制真人轨迹（屏幕覆盖层提示起点/终点，自动起止）
            -dists 500,600  -angles 0,90  -count 3  -dir <库目录>
  list      浏览轨迹库
  pick      按距离/方向挑选真人轨迹（含置信度），没有则回退算法生成
            -dist 500 -angle 0 -tol 30
  replay    回放一条轨迹（可重标定距离/方向/起点）
            -file <json> 或 -dist 500 -angle 0；-to-dist/-to-angle/-start
  selftest  用引擎自身驱动光标，端到端自检录制器（会移动真实鼠标！）

轨迹库固定目录: ~/.mouse-bridge/trajectories （MOUSEBRIDGE_TRAJ_DIR 可覆盖）
角度约定: 0=向右, 90=向下, 180=向左, 270=向上（屏幕坐标，y 向下）
`)
}

// ---------- analyze ----------

func cmdAnalyze(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	reps := fs.Int("reps", 16, "每个 (距离,方向) 单元的重复次数")
	distances := fs.String("distances", joinFloats(trajlab.AnalysisDistances), "逗号分隔的距离列表 (px)")
	angles := fs.String("angles", joinFloats(trajlab.AnalysisAngles), "逗号分隔的方向列表 (度)")
	out := fs.String("out", "", "把矩阵报告写成 JSON 到此路径")
	fs.Parse(args)

	ds := parseFloats(*distances)
	as := parseFloats(*angles)
	if len(ds) == 0 || len(as) == 0 || *reps < 1 {
		fmt.Println("距离/方向/次数不能为空")
		os.Exit(2)
	}
	rep := trajlab.RunMatrix(ds, as, *reps)
	printMatrix(rep)
	if *out != "" {
		b, err := json.MarshalIndent(rep, "", " ")
		if err == nil && os.WriteFile(*out, b, 0o644) == nil {
			fmt.Printf("报告已写入 %s\n", *out)
		} else {
			fmt.Printf("写报告失败: %v\n", err)
		}
	}
	if rep.Verdict != "PASS" {
		os.Exit(1)
	}
}

func printMatrix(rep *trajlab.MatrixReport) {
	fmt.Printf("=== 轨迹验收矩阵: %d 条路径 (%d 距离 × %d 方向 × %d 次) ===\n",
		rep.Paths, len(trajlab.AnalysisDistances), len(trajlab.AnalysisAngles), rep.RepsPerCell)

	type agg struct {
		reps, failReps int
		arcMin, arcMax float64
		latMax         float64
		prMin, prMax   float64
		t2pMin, t2pMax float64
		durMin, durMax float64
		jumpMax        float64
		asymMax        float64
	}
	byDist := map[float64]*agg{}
	var order []float64
	for _, c := range rep.Cells {
		a := byDist[c.Dist]
		if a == nil {
			a = &agg{arcMin: math.Inf(1), arcMax: math.Inf(-1),
				prMin: math.Inf(1), prMax: math.Inf(-1),
				t2pMin: math.Inf(1), t2pMax: math.Inf(-1),
				durMin: math.Inf(1), durMax: math.Inf(-1)}
			byDist[c.Dist] = a
			order = append(order, c.Dist)
		}
		a.reps += c.Reps
		a.failReps += c.FailReps
		a.arcMin = math.Min(a.arcMin, c.ArcChordMin)
		a.arcMax = math.Max(a.arcMax, c.ArcChordMax)
		a.latMax = math.Max(a.latMax, c.MaxSegLateralRatio)
		a.prMin = math.Min(a.prMin, c.PeakRatioMin)
		a.prMax = math.Max(a.prMax, c.PeakRatioMax)
		a.t2pMin = math.Min(a.t2pMin, c.T2PMin)
		a.t2pMax = math.Max(a.t2pMax, c.T2PMax)
		a.durMin = math.Min(a.durMin, c.DurRatioMin)
		a.durMax = math.Max(a.durMax, c.DurRatioMax)
		a.jumpMax = math.Max(a.jumpMax, c.MaxJumpPxS)
		a.asymMax = math.Max(a.asymMax, math.Abs(c.MeanAsym))
	}
	fmt.Println("  距离   失败/路径   弧/弦          段侧偏   峰/均        峰值时刻     时长/Fitts   最大单步")
	for _, d := range order {
		a := byDist[d]
		mark := "  "
		if a.failReps > 0 {
			mark = "⚠ "
		}
		fmt.Printf("%s%5.0f  %3d/%-5d   %.3f-%.3f    %.3f    %.2f-%.2f     %.2f-%.2f     %.2f-%.2f     %.0fpx/s\n",
			mark, d, a.failReps, a.reps, a.arcMin, a.arcMax, a.latMax,
			a.prMin, a.prMax, a.t2pMin, a.t2pMax, a.durMin, a.durMax, a.jumpMax)
	}
	if len(rep.FailByGate) > 0 {
		fmt.Println("  失败门禁分布:")
		for g, n := range rep.FailByGate {
			fmt.Printf("    %-14s %d\n", g, n)
		}
	}
	fmt.Printf("  结论: %s (%d/%d 条路径失败)\n", rep.Verdict, rep.FailPaths, rep.Paths)
}

func joinFloats(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(x, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}

func parseFloats(s string) []float64 {
	var out []float64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if f, err := strconv.ParseFloat(p, 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// ---------- list / pick ----------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := fs.String("dir", trajlab.DefaultDir(), "轨迹库目录")
	fs.Parse(args)
	files, err := trajlab.LoadAll(*dir)
	if err != nil {
		fmt.Println("读取轨迹库失败:", err)
		os.Exit(1)
	}
	fmt.Printf("轨迹库 %s: %d 条\n", *dir, len(files))
	fmt.Println("  文件                          类型      距离    方向   用时    峰速      弧/弦")
	for _, f := range files {
		fmt.Printf("  %-28s  %-7s  %6.0f  %4.0f°  %5.0fms  %6.0fpx/s  %.3f\n",
			baseName(f.File), f.Kind, f.Dist, f.AngleDeg, f.DurationMs, f.PeakSpeed, f.ArcChord)
	}
}

func cmdPick(args []string) {
	fs := flag.NewFlagSet("pick", flag.ExitOnError)
	dist := fs.Float64("dist", 0, "想要的移动距离 (px)")
	angle := fs.Float64("angle", 0, "想要的方向 (度)")
	tol := fs.Float64("tol", 30, "距离容差 (px)")
	dir := fs.String("dir", trajlab.DefaultDir(), "轨迹库目录")
	fs.Parse(args)
	files, err := trajlab.LoadAll(*dir)
	if err != nil {
		fmt.Println("读取轨迹库失败:", err)
		os.Exit(1)
	}
	got := trajlab.Pick(files, *dist, *angle, *tol)
	if got.Traj == nil {
		fmt.Printf("%.0fpx@%.0f°: %s (置信度 0)\n", *dist, *angle, got.Reason)
		return
	}
	fmt.Printf("%.0fpx@%.0f° → %s\n  置信度 %.2f (%s)\n", *dist, *angle, baseName(got.File), got.Confidence, got.Reason)
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		return p[i+1:]
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
