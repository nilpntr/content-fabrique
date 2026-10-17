package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrInputUnreadable is returned (wrapped) when the container user could not read an input mount, or write the output
// directory. The container runs as an unprivileged user, so a 0600 file owned by someone else (root, say) is invisible to
// it. Stage files for a job with a mode the container user can read (0644, or owned by the worker's uid) rather than
// widening permissions on the originals; the provider never changes the mode of anything it did not create.
var ErrInputUnreadable = errors.New("sandbox: mount not accessible to the container user")

// checkAccess verifies, from the host side, that the container user can use every mount: read (and traverse, for a
// directory) for inputs, read, write and traverse for the output directory. Docker would otherwise start the job and the
// tool would fail with a confusing "Permission denied".
func checkAccess(mounts []resolvedMount, user string) error {
	uid, gid, ok := parseUser(user)
	if !ok {
		return nil // a named user: nothing to check from here
	}
	for _, m := range mounts {
		fi, err := os.Stat(m.Host)
		if err != nil {
			return fmt.Errorf("sandbox: mount %s: %w", m.Container, err)
		}
		want := uint32(4) // read
		if fi.IsDir() {
			want |= 1 // traverse
		}
		if m.Writable {
			want |= 2
		}
		if !accessible(fi, uid, gid, want) {
			return fmt.Errorf("%w: %s (mode %v) is not accessible to uid %d gid %d", ErrInputUnreadable, m.Container, fi.Mode().Perm(), uid, gid)
		}
	}
	return nil
}

// accessible reports whether uid:gid holds all of the permission bits in want (r=4, w=2, x=1) on fi, by the owner, group or
// other class that applies. Supplementary groups are not known here, which only makes the check stricter.
func accessible(fi fs.FileInfo, uid, gid int, want uint32) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || uid == 0 {
		return true // not a platform we can check
	}
	mode := uint32(fi.Mode().Perm())
	var have uint32
	switch {
	case int(st.Uid) == uid:
		have = mode >> 6
	case int(st.Gid) == gid:
		have = mode >> 3
	default:
		have = mode
	}
	return have&7&want == want
}

func parseUser(user string) (uid, gid int, ok bool) {
	u, g, found := strings.Cut(user, ":")
	if !found {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	return uid, gid, err1 == nil && err2 == nil
}
