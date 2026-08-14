package api

import "testing"

const mountTable = `proc /proc proc rw,relatime 0 0
/dev/nvme1n1p2 / xfs rw,noatime 0 0
devtmpfs /dev devtmpfs rw,nosuid 0 0
/dev/nvme0n1p3 /mnt/media/sub ntfs3 rw,relatime 0 0
/dev/nvme0n1p3 /mnt/media ntfs3 rw,relatime 0 0
/dev/sda2 /mnt/big\040disk ntfs3 rw 0 0
`

func TestMountpointFor(t *testing.T) {
	cases := []struct {
		name  string
		cands []string
		want  string
	}{
		// a device listed twice (bind mount / subvolume) resolves to the
		// shortest mountpoint, not whichever line comes first
		{"shortest wins", []string{"/dev/nvme0n1p3"}, "/mnt/media"},
		{"root", []string{"/dev/nvme1n1p2"}, "/"},
		{"octal escape", []string{"/dev/sda2"}, "/mnt/big disk"},
		// a symlinked device (/dev/mapper/... → /dev/sda2) matches via the
		// resolved candidate
		{"resolved candidate", []string{"/dev/mapper/vg-x", "/dev/sda2"}, "/mnt/big disk"},
	}
	for _, c := range cases {
		got, err := mountpointFor(mountTable, c.cands...)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMountpointForUnmounted(t *testing.T) {
	if _, err := mountpointFor(mountTable, "/dev/nvme0n1p4"); err == nil {
		t.Fatal("expected an error for an unmounted device")
	}
	if _, err := mountpointFor(mountTable, "", ""); err == nil {
		t.Fatal("expected an error when no candidate is usable")
	}
}

func TestMountpointOf(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		// the *longest* containing mountpoint wins here (the opposite of
		// mountpointFor): a nested subvolume holds the path, its parent does not
		{"nested wins", "/mnt/media/sub/show/ep.mkv", "/mnt/media/sub"},
		{"parent", "/mnt/media/other/ep.mkv", "/mnt/media"},
		{"mountpoint itself", "/mnt/media", "/mnt/media"},
		{"falls back to root", "/home/user/videos", "/"},
		{"octal escape", "/mnt/big disk/rec", "/mnt/big disk"},
		// prefix-but-not-parent: /mnt/media-backup must not match /mnt/media
		{"sibling with shared prefix", "/mnt/media-backup/x", "/"},
	}
	for _, c := range cases {
		if got := mountpointOf(mountTable, c.path); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := mountpointOf("", "/mnt/media"); got != "" {
		t.Errorf("empty table: got %q, want \"\"", got)
	}
}

func TestUnescapeMountField(t *testing.T) {
	cases := map[string]string{
		"/mnt/plain":            "/mnt/plain",
		`/mnt/big\040disk`:      "/mnt/big disk",
		`/mnt/a\011b`:           "/mnt/a\tb",
		`/mnt/back\134slash`:    "/mnt/back\\slash",
		`/mnt/trailing\`:        `/mnt/trailing\`,
		`/mnt/not\999octal`:     `/mnt/not\999octal`,
		`/mnt/two\040\040space`: "/mnt/two  space",
	}
	for in, want := range cases {
		if got := unescapeMountField(in); got != want {
			t.Errorf("unescapeMountField(%q) = %q, want %q", in, got, want)
		}
	}
}
