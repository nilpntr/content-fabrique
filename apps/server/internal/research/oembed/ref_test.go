package oembed

import (
	"context"
	"strings"
	"testing"
)

// The same post linked with another case of the handle uses the same fixture, and the RawRef does not follow the signed thumbnail link
// (which changes on every request), so a live re-import stores nothing twice.
func TestTheSamePostGivesTheSameRef(t *testing.T) {
	c, err := New(Config{Mode: Replay, Dir: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Resolve(context.Background(), postURL)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := New(Config{Mode: Replay, Dir: fixtures})
	b, err := c2.Resolve(context.Background(), strings.Replace(postURL, "synthetic_lifter_a", "Synthetic_Lifter_A", 1))
	if err != nil || b.RawRef != a.RawRef {
		t.Fatalf("case variant: %+v %v", b, err)
	}
	one := &fakeTikTok{body: answer("synthetic_lifter_a", "https://p16-sign-va.tiktokcdn.com/obj/x~tplv.image?x-expires=1&x-signature=a")}
	two := &fakeTikTok{body: answer("synthetic_lifter_a", "https://p16-sign-va.tiktokcdn.com/obj/x~tplv.image?x-expires=2&x-signature=b")}
	l1, _ := newLive(t, one, "")
	l2, _ := newLive(t, two, "")
	r1, err1 := l1.Resolve(context.Background(), postURL)
	r2, err2 := l2.Resolve(context.Background(), postURL)
	if err1 != nil || err2 != nil || r1.RawRef != r2.RawRef || r1.ThumbnailURL == r2.ThumbnailURL {
		t.Fatalf("signed thumbnails: %v %v %s %s", err1, err2, r1.RawRef, r2.RawRef)
	}
}
