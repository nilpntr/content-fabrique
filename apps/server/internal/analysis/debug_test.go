package analysis

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/media"
)

func mediaArgs(in string) []string {
	a := media.AnalysisArgs(in)
	for i, s := range a {
		if s == "/work/thumbs.raw" {
			a[i] = "thumbs.raw"
		}
	}
	return a
}

func loadRaw(t *testing.T, name string) Raw {
	t.Helper()
	dir := "/tmp/cf-analysis/raw/" + name
	if _, err := os.Stat(filepath.Join(dir, "thumbs.raw")); err != nil {
		args := mediaArgs("/tmp/cf-analysis/proxies/" + name + ".mp4")
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	rd := func(n string) []byte { b, _ := os.ReadFile(filepath.Join(dir, n)); return b }
	raw, err := ParseRaw(rd("thumbs.raw"), rd("sharp.txt"), rd("scene.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sparkline(x []float64, per int, max float64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for i := 0; i < len(x); i += per {
		m := 0.0
		n := 0
		for j := i; j < i+per && j < len(x); j++ {
			m += x[j]
			n++
		}
		m /= float64(n)
		k := int(m / max * 7.99)
		k = min(max2(k, 0), 7)
		b.WriteRune(bars[k])
	}
	return b.String()
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestDebugDump(t *testing.T) {
	name := os.Getenv("CF_DEBUG_CLIP")
	if name == "" {
		t.Skip()
	}
	raw := loadRaw(t, name)
	s := BuildSeries(raw)
	fmt.Println("motion p50/p90/max", quantile(s.Motion, .5), quantile(s.Motion, .9), quantile(s.Motion, 1))
	fmt.Println("luma", quantile(s.Luma, .1), quantile(s.Luma, .5), quantile(s.Luma, .9))
	fmt.Println("sharp", quantile(s.Sharp, .1), quantile(s.Sharp, .5), quantile(s.Sharp, .9))
	var cuts []string
	for i, v := range s.Scene {
		if v > 5 {
			cuts = append(cuts, fmt.Sprintf("%.1fs:%.1f", float64(i)/10, v))
		}
	}
	fmt.Println("scene>5", cuts)
	debugOn = true
	debugf = func(f string, a ...any) { fmt.Printf(f, a...) }
	res := FindReps(raw, s, DefaultParams())
	res.Dump(os.Stdout)
}
