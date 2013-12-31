package fetch

import (
	"net/url"
	"testing"
)

func TestFetcher(t *testing.T) {
	urls := []string{"http://toto.com", "https://google.com"}
	f := New(nil, -1)
	ch := f.Start()
	for _, u := range urls {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		ch <- &Request{parsed, "GET"}
	}
}
