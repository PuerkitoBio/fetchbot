// Copyright 2014 Martin Angers and Contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fetchbot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type traceCmd struct {
	*Cmd
	Trace string
}

// Avoid data races
var mu sync.Mutex

func setTrace(l string) Handler {
	return HandlerFunc(func(ctx *Context, res *http.Response, err error) {
		if err != nil && testing.Verbose() {
			fmt.Println(err)
		}
		mu.Lock()
		defer mu.Unlock()
		ctx.Cmd.(*traceCmd).Trace = l
	})
}

func TestMux(t *testing.T) {
	// Start the servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Write([]byte(`
User-agent: *
Disallow: /deny
`))
			return
		case "/204":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(204)
			w.Write(nil)
			return
		case "/4xx":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(404)
			w.Write(nil)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.Write([]byte("1"))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv2.Close()
	srvu, err := url.Parse(srv1.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv1Host := srvu.Host
	srvu, err = url.Parse(srv2.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv2Host := srvu.Host

	// List of test cases
	cases := []struct {
		url   string
		trace string
	}{
		0: {srv2.URL + "/none", "d"}, // no specific handler, use default
		1: {srv1.URL + "/json", "j"}, // json-specific handler
		2: {srv1.URL + "/deny", "a"}, // ErrDisallowed, use any errors handler
		3: {srv1.URL + "/a", "g"},    // GET text/html
		4: {srv1.URL + "/204", "s"},  // status 204
		5: {srv1.URL + "/4xx", "r"},  // status range 4xx
		6: {srv1.URL + "/b", "p"},    // path-specific handler
		7: {srv1.URL + "/baba", "q"}, // path-specific handler
		8: {srv1.URL + "/b/c", "p"},  // path-specific handler
		9: {srv2.URL + "/zz", "r"},   // custom predicate
	}
	// Start the fetcher
	mux := NewMux()
	mux.HandleError(ErrEmptyHost, setTrace("e"))
	mux.HandleErrors(setTrace("a"))
	mux.DefaultHandler = setTrace("d")
	mux.Response().ContentType("application/json").Handler(setTrace("j"))
	mux.Response().Host(srv1Host).ContentType("text/html").Method("GET").Handler(setTrace("g"))
	mux.Response().ContentType("text/html").Method("HEAD").Handler(setTrace("h"))
	mux.Response().Host(srv1Host).Status(204).Handler(setTrace("s"))
	mux.Response().Host(srv1Host).StatusRange(400, 499).Handler(setTrace("r"))
	mux.Response().Path("/b").Handler(setTrace("p"))
	mux.Response().Path("/ba").Handler(setTrace("q"))
	mux.Response().Custom(func(res *http.Response) bool {
		return strings.Contains(res.Request.URL.Path, "zz")
	}).Handler(setTrace("r"))
	mux.Response().Host(srv2Host).Path("/b").Handler(setTrace("z"))
	f := New(mux)
	f.CrawlDelay = 0
	for i, c := range cases {
		parsed, err := url.Parse(c.url)
		if err != nil {
			t.Errorf("%d: error parsing url: %s", i, err)
			continue
		}
		cmd := &traceCmd{&Cmd{U: parsed, M: "GET"}, ""}

		q := f.Start()
		if err := q.Send(cmd); err != nil {
			t.Fatal(err)
		}
		q.Close()

		// make sure the call is out of the mux's handler
		mux.mu.Lock()
		mux.mu.Unlock()

		mu.Lock()
		if cmd.Trace != c.trace {
			t.Errorf("%d: expected trace '%s', got '%s'", i, c.trace, cmd.Trace)
		}
		mu.Unlock()
	}
}
