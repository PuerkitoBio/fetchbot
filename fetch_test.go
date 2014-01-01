package fetch

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

var nopHandler Handler = HandlerFunc(func(cmd Command, res *http.Response, req *http.Request, err error) {})

func LogHandler(w io.Writer) Handler {
	return HandlerFunc(func(cmd Command, res *http.Response, req *http.Request, err error) {
		fmt.Fprintf(w, "fetch: %s %s\n", cmd.Method(), cmd.URL())
		if err != nil {
			fmt.Fprintf(w, "\terror: %s\n", err)
		} else {
			fmt.Fprintf(w, "\t%d - %s\n", res.StatusCode, res.Status)
			for k, vs := range res.Header {
				fmt.Fprintf(w, "\t%s: %v\n", k, vs)
			}
		}
	})
}

func TestIdleList(t *testing.T) {
	f := New(nopHandler, -1)
	f.WorkerIdleTTL = 2 * time.Second
	f.Start()
	err := f.EnqueueString("http://0value.com", "GET")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	err = f.EnqueueString("http://0value.com/a", "GET")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	err = f.EnqueueString("http://google.com/", "GET")
	if err != nil {
		t.Fatal(err)
	}
	f.Stop()
}

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
