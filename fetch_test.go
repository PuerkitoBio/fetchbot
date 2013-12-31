package fetch

import (
	"net/url"
	"os"
	"testing"
)

func TestFetcher(t *testing.T) {
	urls := []string{"http://0value.com", "https://google.com"}
	f := New(LogHandler(os.Stdout), -1)
	f.Start()
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		f.Enqueue(parsed, "GET")
	}
	f.Stop()
	if len(f.hosts) != 2 {
		t.Errorf("expected to have 2 hosts in the map, got %d", len(f.hosts))
	}
}
