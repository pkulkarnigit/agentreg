package web

import (
	"testing"
	"time"
)

func TestTimeAgo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{1 * time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{1 * time.Hour, "1 hour ago"},
		{3 * time.Hour, "3 hours ago"},
		{25 * time.Hour, "1 day ago"},
		{5 * 24 * time.Hour, "5 days ago"},
		{45 * 24 * time.Hour, "1 month ago"},
		{400 * 24 * time.Hour, "1 year ago"},
	}
	for _, c := range cases {
		got := timeAgo(now.Add(-c.ago))
		if got != c.want {
			t.Errorf("timeAgo(%s ago) = %q, want %q", c.ago, got, c.want)
		}
	}
}
