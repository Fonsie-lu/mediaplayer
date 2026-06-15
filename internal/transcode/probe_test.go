package transcode

import "testing"

func TestPickPreferredAudio(t *testing.T) {
	cases := []struct {
		name   string
		tracks []AudioTrack
		want   int
	}{
		{"empty", nil, 0},
		{"english wins over default", []AudioTrack{
			{Index: 0, Language: "ger", Default: true},
			{Index: 1, Language: "eng"},
		}, 1},
		{"default when no english", []AudioTrack{
			{Index: 0, Language: "ger"},
			{Index: 1, Language: "fre", Default: true},
		}, 1},
		{"first as fallback", []AudioTrack{
			{Index: 0, Language: "ger"},
			{Index: 1, Language: "fre"},
		}, 0},
	}
	for _, c := range cases {
		if got := pickPreferredAudio(c.tracks); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
