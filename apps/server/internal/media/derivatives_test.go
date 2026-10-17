package media

import (
	"strings"
	"testing"
)

func iphonePortrait() Metadata {
	sel := 1
	return Metadata{
		HasVideo: true, VideoIndex: 0, Width: 2160, Height: 3840, CodedWidth: 3840, CodedHeight: 2160, Rotation: -90,
		FPS: FPS{Rational: Rational{Num: 30, Den: 1}, Value: 30}, ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709",
		HasAudio: true, SelectedAudio: &sel,
		AudioStreams: []AudioStream{
			{Index: 1, Codec: "aac", Channels: 1, Default: true, Decodable: true},
			{Index: 2, Codec: "unknown", CodecTag: "apac", Decodable: false},
		},
	}
}

func join(a []string) string { return strings.Join(a, " ") }

const (
	inOpts  = "-protocol_whitelist file -format_whitelist " + wl
	wl      = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi,mpegts,mpeg,flv,asf,mp3,aac,wav,flac,ogg"
	prefix  = "ffmpeg -nostdin -hide_banner -v error -y "
	colours = "-colorspace bt709 -color_primaries bt709 -color_trc bt709 -color_range tv"
)

func TestProxyArgsGolden(t *testing.T) {
	// iPhone portrait, SDR: the APAC track (stream 2) is never mapped, the AAC one (stream 1) is.
	a, err := ProxyArgs("/in/0", "/work/proxy_720.mp4", iphonePortrait())
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + inOpts + " -i /in/0 -map 0:0 -map 0:1 -sn -dn -map_metadata -1 -map_chapters -1 " +
		"-vf scale=720:1280:flags=lanczos,setsar=1,format=yuv420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv " +
		"-c:v libx264 -preset veryfast -crf 23 -profile:v high -pix_fmt yuv420p -maxrate 8M -bufsize 16M " +
		"-fps_mode cfr -r 30 -g 30 -keyint_min 30 -sc_threshold 0 " + colours +
		" -c:a aac -b:a 128k -movflags +faststart /work/proxy_720.mp4"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(join(a), "0:2") {
		t.Fatal("the APAC stream must never be mapped")
	}
}

func TestProxyArgsHDRAndNoAudio(t *testing.T) {
	m := iphonePortrait()
	m.HDR, m.HDRFormat, m.ColorTransfer, m.ColorPrimaries, m.ColorSpace = true, "hlg", "arib-std-b67", "bt2020", "bt2020nc"
	m.SelectedAudio, m.AudioStreams = nil, []AudioStream{{Index: 2, Codec: "unknown", CodecTag: "apac"}}
	m.FPS = FPS{Rational: Rational{Num: 30000, Den: 1001}, Value: 29.97}
	a, err := ProxyArgs("/in/0", "/work/p.mp4", m)
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + inOpts + " -i /in/0 -map 0:0 -sn -dn -map_metadata -1 -map_chapters -1 " +
		"-vf scale=720:1280:flags=lanczos,setsar=1,zscale=tin=arib-std-b67:min=2020_ncl:pin=2020:rin=tv:t=linear:npl=100," +
		"format=gbrpf32le,zscale=p=709,tonemap=tonemap=hable:desat=0,zscale=t=709:m=709:r=tv,format=yuv420p,setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv " +
		"-c:v libx264 -preset veryfast -crf 23 -profile:v high -pix_fmt yuv420p -maxrate 8M -bufsize 16M " +
		"-fps_mode cfr -r 30 -g 30 -keyint_min 30 -sc_threshold 0 " + colours +
		" -movflags +faststart /work/p.mp4"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	m.HDRFormat = "pq"
	a, _ = ProxyArgs("/in/0", "/work/p.mp4", m)
	if !strings.Contains(join(a), "zscale=tin=smpte2084:") {
		t.Fatalf("PQ must name its transfer: %s", join(a))
	}
}

func TestProxyArgsOtherColourAndSurround(t *testing.T) {
	m := iphonePortrait()
	m.ColorSpace, m.ColorPrimaries, m.ColorTransfer, m.PixFmt = "smpte170m", "smpte170m", "smpte170m", "yuv420p"
	m.AudioStreams[0].Channels = 6
	a, err := ProxyArgs("/in/0", "/work/p.mp4", m)
	if err != nil {
		t.Fatal(err)
	}
	j := join(a)
	if !strings.Contains(j, "zscale=min=170m:pin=170m:tin=601:t=709:m=709:p=709:r=tv,format=yuv420p,"+"setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv ") || !strings.Contains(j, "-c:a aac -b:a 128k -ac 2 ") {
		t.Fatalf("%s", j)
	}
	if _, err := ProxyArgs("/in/0", "/work/p.mp4", Metadata{}); err == nil {
		t.Fatal("no video must be refused")
	}
}

func TestScaledSize(t *testing.T) {
	for _, c := range []struct{ w, h, short, ww, hh int }{
		{2160, 3840, 720, 720, 1280},
		{3840, 2160, 720, 1280, 720},
		{1080, 1920, 360, 360, 640},
		{480, 270, 720, 480, 270},   // never scaled up
		{481, 271, 720, 480, 270},   // odd sizes are made even
		{1000, 1000, 720, 720, 720}, // square
		{2000, 1001, 720, 1438, 720},
		{0, 10, 720, 0, 0},
	} {
		if w, h := ScaledSize(c.w, c.h, c.short); w != c.ww || h != c.hh {
			t.Errorf("ScaledSize(%d,%d,%d) = %dx%d, want %dx%d", c.w, c.h, c.short, w, h, c.ww, c.hh)
		}
	}
}

func TestGOP(t *testing.T) {
	for fps, want := range map[float64]int{30: 30, 29.97: 30, 29.13: 30, 23.976: 24, 24: 24, 25: 25, 59.94: 60, 120: 120, 0: 30, 1000: 240, 0.2: 1} {
		if got := GOP(fps); got != want {
			t.Errorf("GOP(%v) = %d, want %d", fps, got, want)
		}
	}
}

func TestPosterAndDecodeCheckGolden(t *testing.T) {
	if got, want := join(PosterArgs("/in/0", "/work/poster.jpg", 8713)), prefix+inOpts+" -ss 8.713 -i /in/0 -map 0:v:0 -an -vf thumbnail=24 -frames:v 1 -update 1 -q:v 3 /work/poster.jpg"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	a, err := DecodeCheckArgs("/in/0", iphonePortrait())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := join(a), prefix+"-xerror "+inOpts+" -i /in/0 -map 0:0 -frames:v 3 -an -f null -"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	for in, want := range map[int64]int64{87135: 8713, 11000: 1100, 600: 60, 100: 0, 0: 0, 700: 70, 400: 0} {
		if got := PosterAtMs(in); got != want {
			t.Errorf("PosterAtMs(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestSpriteGridAndVTT(t *testing.T) {
	g, err := NewSpriteGrid(87135, 720, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if g.IntervalSec != 1 || g.Count != 88 || g.Cols != 10 || g.Rows != 9 || g.TileW != 160 || g.TileH != 284 || g.Width() != 1600 || g.Height() != 2556 {
		t.Fatalf("%+v", g)
	}
	a, err := SpriteArgs("/in/0", "/work/sprite.jpg", g)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := join(a), prefix+inOpts+" -i /in/0 -map 0:v:0 -an -vf fps=1/1,scale=160:284:flags=bicubic,tile=10x9 -frames:v 1 -update 1 -q:v 5 /work/sprite.jpg"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	vtt := SpriteVTT(g, 87135, "sprite.jpg")
	if !strings.HasPrefix(vtt, "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nsprite.jpg#xywh=0,0,160,284\n\n00:00:01.000 --> 00:00:02.000\nsprite.jpg#xywh=160,0,160,284\n") {
		t.Fatalf("%s", vtt[:200])
	}
	if !strings.HasSuffix(vtt, "00:01:27.000 --> 00:01:27.135\nsprite.jpg#xywh=1120,2272,160,284\n") {
		t.Fatalf("last cue: %q", vtt[len(vtt)-90:])
	}
	if n := strings.Count(vtt, "-->"); n != 88 {
		t.Fatalf("%d cues", n)
	}

	// A long clip gets a longer interval so the sheet stays at 100 tiles; a short one gets a narrower sheet.
	g, _ = NewSpriteGrid(3600*1000, 1920, 1080)
	if g.IntervalSec != 36 || g.Count != 100 || g.Rows != 10 || g.TileW != 160 || g.TileH != 90 {
		t.Fatalf("%+v", g)
	}
	g, _ = NewSpriteGrid(3500, 1080, 1920)
	if g.IntervalSec != 1 || g.Count != 4 || g.Cols != 4 || g.Rows != 1 {
		t.Fatalf("%+v", g)
	}
	// A very tall picture keeps its aspect ratio inside the tile height limit.
	g, _ = NewSpriteGrid(5000, 100, 1000)
	if g.TileH != 480 || g.TileW != 48 {
		t.Fatalf("%+v", g)
	}
	if _, err := NewSpriteGrid(0, 1, 1); err == nil {
		t.Fatal("no duration must be refused")
	}
	if _, err := SpriteArgs("a", "b", SpriteGrid{}); err == nil {
		t.Fatal("an empty grid must be refused")
	}
}

func TestHoverSegments(t *testing.T) {
	if got := HoverSegments(5000); len(got) != 1 || got[0] != (Segment{0, 5000}) {
		t.Fatalf("%v", got)
	}
	if got := HoverSegments(8000); len(got) != 1 || got[0].LenMs != 8000 {
		t.Fatalf("exactly 8 s is used whole: %v", got)
	}
	want := []Segment{{1000, 2000}, {5000, 2000}, {9000, 2000}, {13000, 2000}}
	got := HoverSegments(16000)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("16 s: %v, want %v", got, want)
		}
	}
	for _, d := range []int64{8001, 9000, 11000, 87135, 3600000} {
		segs := HoverSegments(d)
		if len(segs) != 4 {
			t.Fatalf("%d ms: %v", d, segs)
		}
		var total int64
		for i, s := range segs {
			total += s.LenMs
			if s.StartMs < 0 || s.StartMs+s.LenMs > d {
				t.Fatalf("%d ms: segment %v is outside the clip", d, s)
			}
			if i > 0 && s.StartMs < segs[i-1].StartMs+segs[i-1].LenMs {
				t.Fatalf("%d ms: segments overlap: %v", d, segs)
			}
		}
		if total != HoverTotalMs {
			t.Fatalf("%d ms: %d ms of preview", d, total)
		}
	}
}

func TestHoverArgsGolden(t *testing.T) {
	a, err := HoverArgs("/in/0", "/work/hover_preview.mp4", HoverSegments(87135), 720, 1280, 30)
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + inOpts + " -ss 9.891 -t 2.000 -i /in/0" + " -ss 31.675 -t 2.000 -i /in/0" +
		" -ss 53.459 -t 2.000 -i /in/0" + " -ss 75.243 -t 2.000 -i /in/0" +
		" -filter_complex [0:v:0]setpts=PTS-STARTPTS[v0];[1:v:0]setpts=PTS-STARTPTS[v1];[2:v:0]setpts=PTS-STARTPTS[v2];[3:v:0]setpts=PTS-STARTPTS[v3];" +
		"[v0][v1][v2][v3]concat=n=4:v=1:a=0,scale=360:640:flags=lanczos,setsar=1,format=yuv420p[v] -map [v]" +
		" -an -sn -dn -map_metadata -1 -map_chapters -1 -c:v libx264 -preset medium -profile:v main -pix_fmt yuv420p -crf 30 -maxrate 600k -bufsize 1200k" +
		" -g 30 -keyint_min 30 -sc_threshold 0 " + colours + " -movflags +faststart /work/hover_preview.mp4"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	a, err = HoverArgs("/in/0", "/work/h.mp4", HoverSegments(5000), 1280, 720, 25)
	if err != nil {
		t.Fatal(err)
	}
	want = prefix + inOpts + " -ss 0.000 -t 5.000 -i /in/0 -map 0:v:0 -vf scale=640:360:flags=lanczos,setsar=1,format=yuv420p" +
		" -an -sn -dn -map_metadata -1 -map_chapters -1 -c:v libx264 -preset medium -profile:v main -pix_fmt yuv420p -crf 30 -maxrate 600k -bufsize 1200k" +
		" -g 25 -keyint_min 25 -sc_threshold 0 " + colours + " -movflags +faststart /work/h.mp4"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := HoverArgs("a", "b", nil, 1, 1, 30); err == nil {
		t.Fatal("no segments must be refused")
	}
}

func TestAudio16kArgsGolden(t *testing.T) {
	a, err := Audio16kArgs("/in/0", "/work/audio_16k.wav", iphonePortrait())
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + inOpts + " -i /in/0 -map 0:1 -vn -sn -dn -map_metadata -1 -map_chapters -1 -ac 1 -ar 16000 -c:a pcm_s16le -f wav /work/audio_16k.wav"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	m := iphonePortrait()
	m.SelectedAudio = nil
	if _, err := Audio16kArgs("a", "b", m); err == nil {
		t.Fatal("no audio must be refused")
	}
}

func TestImageProbeAndPosterGolden(t *testing.T) {
	if got := join(ProbeImageArgs("/in/0")); !strings.Contains(got, "-format_whitelist "+ImageFormats+" ") || strings.Contains(got, "mov") {
		t.Fatal(got)
	}
	a, err := ImagePosterArgs("/in/0", "/work/poster.jpg", Metadata{HasVideo: true, Width: 4000, Height: 3000})
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + "-protocol_whitelist file -format_whitelist " + ImageFormats +
		" -i /in/0 -map 0:0 -an -map_metadata -1 -vf scale=960:720:flags=lanczos,setsar=1 -frames:v 1 -update 1 -q:v 3 /work/poster.jpg"
	if got := join(a); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// Every derivative that reads a file restricts the demuxers and protocols first (the original is untrusted; the
// intermediate files are ours, but one rule is easier to keep).
func TestDerivativeBuildersRestrictInput(t *testing.T) {
	m := iphonePortrait()
	g, _ := NewSpriteGrid(10000, 720, 1280)
	proxy, _ := ProxyArgs("/in/0", "/work/o", m)
	sprite, _ := SpriteArgs("/in/0", "/work/o", g)
	hover, _ := HoverArgs("/in/0", "/work/o", HoverSegments(20000), 720, 1280, 30)
	audio, _ := Audio16kArgs("/in/0", "/work/o", m)
	check, _ := DecodeCheckArgs("/in/0", m)
	for name, args := range map[string][]string{
		"proxy": proxy, "poster": PosterArgs("/in/0", "/work/o", 0), "sprite": sprite, "hover": hover, "audio": audio, "check": check,
	} {
		j := join(args)
		if !strings.Contains(j, "-protocol_whitelist file -format_whitelist "+UntrustedFormats+" ") {
			t.Errorf("%s does not restrict its input: %s", name, j)
		}
		for _, bad := range []string{"lavfi", "concat:", "http", "-map 0:2", "0:a:0"} {
			if strings.Contains(j, bad) {
				t.Errorf("%s contains %q: %s", name, bad, j)
			}
		}
	}
}
