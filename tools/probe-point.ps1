Add-Type @"
using System;
using System.Runtime.InteropServices;
public class WProbe {
  [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(POINT p);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, System.Text.StringBuilder s, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr h, System.Text.StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X, Y; }
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L, T, R, B; }
}
"@
$p = New-Object WProbe+POINT; $p.X = 700; $p.Y = 500
$hw = [WProbe]::WindowFromPoint($p)
$sb = New-Object System.Text.StringBuilder 256
[void][WProbe]::GetWindowText($hw, $sb, 256); $title = $sb.ToString()
[void][WProbe]::GetClassName($hw, $sb, 256); $cls = $sb.ToString()
$r = New-Object WProbe+RECT; [void][WProbe]::GetWindowRect($hw, [ref]$r)
$fg = [WProbe]::GetForegroundWindow()
$sb2 = New-Object System.Text.StringBuilder 256
[void][WProbe]::GetWindowText($fg, $sb2, 256); $fgTitle = $sb2.ToString()
Write-Output "at(700,500): hwnd=$hw class=$cls title=$title rect=($($r.L),$($r.T))-($($r.R),$($r.B))"
Write-Output "foreground:  hwnd=$fg title=$fgTitle"
# also check the btn-left landing point and page center
$p2 = New-Object WProbe+POINT; $p2.X = 489; $p2.Y = 410
$hw2 = [WProbe]::WindowFromPoint($p2)
$sb3 = New-Object System.Text.StringBuilder 256
[void][WProbe]::GetClassName($hw2, $sb3, 256); $cls2 = $sb3.ToString()
Write-Output "at(489,410): hwnd=$hw2 class=$cls2"
