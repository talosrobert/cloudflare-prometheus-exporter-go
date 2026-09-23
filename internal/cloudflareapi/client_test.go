package cloudflareapi

import "testing"

func TestTagFilter_QueryString(t *testing.T) {
	cases := []struct {
		name string
		f    TagFilter
		want string
	}{
		{"key only", TagFilter{Key: "archived"}, "archived"},
		{"key value", TagFilter{Key: "env", Value: "prod"}, "env=prod"},
		{"negate key", TagFilter{Key: "archived", Negate: true}, "!archived"},
		{"negate key value", TagFilter{Key: "region", Value: "us-west-1", Negate: true}, "region!=us-west-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.queryString(); got != tc.want {
				t.Errorf("queryString() = %q, want %q", got, tc.want)
			}
		})
	}
}
