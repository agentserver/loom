package handlepick

import "testing"

func TestFirstURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`Compress {"type":"image_url","url":"http://x/y","bytes":12}`, "http://x/y", true},
		{`Use this https://example.com/img.png as input`, "https://example.com/img.png", true},
		{`no urls in here`, "", false},
		{``, "", false},
		{`see http://a.b/c.`, "http://a.b/c", true},
		{`(http://a.b/c)`, "http://a.b/c", true},
		{`first http://a.b/c then http://d.e/f`, "http://a.b/c", true},
	}
	for _, c := range cases {
		got, ok := FirstURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("FirstURL(%q) = (%q, %v); want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
