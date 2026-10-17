package analysis

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDebugMotion(t *testing.T) {
	name := os.Getenv("CF_DEBUG_MOTION")
	if name == "" {
		t.Skip()
	}
	s := BuildSeries(loadRaw(t, name))
	sm := smooth(s.Motion, 5)
	for i := 0; i < len(sm) && i < 200; i += 5 {
		fmt.Printf("%5.1fs motion %.2f luma %.1f sharp %.2f contrast %.0f\n", float64(i)/10, sm[i], s.Luma[i], s.Sharp[i], s.Contrast[i])
	}
	fmt.Println(quantile(sm, .05), quantile(sm, .1), quantile(sm, .25), quantile(sm, .5))
}

func TestDebugAnalyze(t *testing.T) {
	name := os.Getenv("CF_DEBUG_ANALYZE")
	if name == "" {
		t.Skip()
	}
	raw := loadRaw(t, name)
	h := Hints{Words: strings.Fields(strings.ReplaceAll(name, "-", " "))}
	a := Analyze(raw, h, DefaultParams(), DefaultShotParams())
	fmt.Printf("== %s: %d shots, %s\n", name, len(a.Shots), a.Summary.Line())
	for _, m := range a.Moments {
		fmt.Printf("%-5s %6.1f-%6.1fs  %.2f  %s\n", m.Kind, float64(m.StartMs)/1000, float64(m.EndMs)/1000, m.Score, m.Description)
	}
}
