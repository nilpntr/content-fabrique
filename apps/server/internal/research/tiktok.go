package research

import (
	"fmt"
	"strconv"
	"time"
)

// tiktokEpochFloor is 2016-01-01: TikTok (and its predecessor's id scheme) has no post before it, so an id that decodes to an earlier time
// is not a post id with a time in it.
const tiktokEpochFloor = 1451606400

// TikTokPostTime returns when a TikTok post was published, read from its id: a TikTok post id is a 64-bit number whose top 32 bits are
// the Unix time of the post in seconds. No network call: this is how a post gets posted_at_source "id". It refuses an id that is not a
// number or decodes to a time before 2016.
func TikTokPostTime(id string) (time.Time, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("research: TikTok post id %q is not a 64-bit number", clip(id))
	}
	sec := int64(n >> 32) //nolint:gosec // n >> 32 fits in 32 bits
	if sec < tiktokEpochFloor {
		return time.Time{}, fmt.Errorf("research: TikTok post id %q carries no post time", clip(id))
	}
	return time.Unix(sec, 0).UTC(), nil
}
