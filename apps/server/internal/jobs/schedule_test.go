package jobs

import (
	"testing"
	"time"
)

func TestDailyAtFiresOncePerDayAtTheSameUTCTime(t *testing.T) {
	d := dailyAt{hour: 3, minute: 15}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-10-17T00:00:00Z", "2026-10-17T03:15:00Z"},
		{"2026-10-17T03:14:59Z", "2026-10-17T03:15:00Z"},
		{"2026-10-17T03:15:00Z", "2026-10-18T03:15:00Z"}, // strictly after
		{"2026-10-17T23:59:59Z", "2026-10-18T03:15:00Z"},
		{"2026-10-17T05:00:00+02:00", "2026-10-17T03:15:00Z"}, // 03:00 UTC
		{"2026-12-31T12:00:00Z", "2027-01-01T03:15:00Z"},
	} {
		if got := d.Next(at(tc.now)); !got.Equal(at(tc.want)) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got.Format(time.RFC3339), tc.want)
		}
	}
}
