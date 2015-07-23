// Copyright 2014 Martin Angers and Contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fetchbot

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

type basicAuthCmd struct {
	*Cmd
	user, pwd string
}

func (ba *basicAuthCmd) BasicAuth() (string, string) {
	return ba.user, ba.pwd
}

func TestBasicAuth(t *testing.T) {
	creds := base64.StdEncoding.EncodeToString([]byte("me:you"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth := req.Header.Get("Authorization")
		if auth != "Basic "+creds {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"Authorization Required\"")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	cases := []struct {
		cmd    Command
		status int
	}{
		0: {
			&basicAuthCmd{&Cmd{U: mustParse(t, srv.URL+"/a"), M: "GET"}, "me", "you"},
			http.StatusOK,
		},
		1: {
			&Cmd{U: mustParse(t, srv.URL+"/b"), M: "GET"},
			http.StatusUnauthorized,
		},
		2: {
			&basicAuthCmd{&Cmd{U: mustParse(t, srv.URL+"/c"), M: "GET"}, "some", "other"},
			http.StatusUnauthorized,
		},
		3: {
			&readerCmd{&Cmd{U: mustParse(t, srv.URL+"/d"), M: "POST"},
				strings.NewReader("a")},
			http.StatusUnauthorized,
		},
		4: {
			&valuesCmd{&Cmd{U: mustParse(t, srv.URL+"/e"), M: "POST"},
				url.Values{"k": {"v"}}},
			http.StatusUnauthorized,
		},
	}
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for i, c := range cases {
		if err := q.Send(c.cmd); err != nil {
			t.Errorf("%d: error sending command: %s", i, err)
		}
	}
	q.Close()
	var urls []string
	for i, c := range cases {
		urls = append(urls, c.cmd.URL().String())
		if st := sh.StatusFor(c.cmd.URL().String()); st != c.status {
			t.Errorf("%d: expected status %d, got %d", i, c.status, st)
		}
	}
	if !sh.CalledWithExactly(urls...) {
		t.Error("expected handler to be called for all cases")
	}
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no error, got %d", cnt)
	}
}

type readerCmd struct {
	*Cmd
	r io.Reader
}

func (rc *readerCmd) Reader() io.Reader {
	return rc.r
}

type valuesCmd struct {
	*Cmd
	vals url.Values
}

func (vc *valuesCmd) Values() url.Values {
	return vc.vals
}

type cookiesCmd struct {
	*Cmd
	cooks []*http.Cookie
}

func (cc *cookiesCmd) Cookies() []*http.Cookie {
	return cc.cooks
}

func TestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cooks := req.Cookies()
		if len(cooks) == 0 {
			b, err := ioutil.ReadAll(req.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
			w.Write(b)
		} else {
			for i, c := range cooks {
				if i > 0 {
					w.Write([]byte{'&'})
				}
				w.Write([]byte(c.Name))
			}
		}
	}))
	defer srv.Close()
	cases := []struct {
		cmd  Command
		body string
	}{
		0: {
			&readerCmd{&Cmd{U: mustParse(t, srv.URL+"/a"), M: "POST"},
				strings.NewReader("a")},
			"a",
		},
		1: {
			&valuesCmd{&Cmd{U: mustParse(t, srv.URL+"/b"), M: "POST"},
				url.Values{"k": {"v"}}},
			"k=v",
		},
		2: {
			&Cmd{U: mustParse(t, srv.URL+"/c"), M: "POST"},
			"",
		},
		3: {
			&basicAuthCmd{&Cmd{U: mustParse(t, srv.URL+"/d"), M: "POST"}, "me", "you"},
			"",
		},
		4: {
			&cookiesCmd{&Cmd{U: mustParse(t, srv.URL+"/e"), M: "GET"},
				[]*http.Cookie{&http.Cookie{Name: "e"}}},
			"e",
		},
		5: {
			&cookiesCmd{&Cmd{U: mustParse(t, srv.URL+"/f"), M: "GET"},
				[]*http.Cookie{&http.Cookie{Name: "f1"}, &http.Cookie{Name: "f2"}}},
			"f1&f2",
		},
	}
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for i, c := range cases {
		if err := q.Send(c.cmd); err != nil {
			t.Errorf("%d: error sending command: %s", i, err)
		}
	}
	q.Close()
	var urls []string
	for i, c := range cases {
		urls = append(urls, c.cmd.URL().String())
		if b := sh.BodyFor(c.cmd.URL().String()); b != c.body {
			t.Errorf("%d: expected body '%s', got '%s'", i, c.body, b)
		}
	}
	if !sh.CalledWithExactly(urls...) {
		t.Error("expected handler to be called for all cases")
	}
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no error, got %d", cnt)
	}
}

type headerCmd struct {
	*Cmd
	hdr http.Header
}

func (hc *headerCmd) Header() http.Header {
	return hc.hdr
}

func TestHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Write headers in lexical order so that result is predictable
		keys := make([]string, 0, len(req.Header))
		for k := range req.Header {
			if len(k) == 1 {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			w.Write([]byte(fmt.Sprintf("%s:%s\n", k, req.Header[k][0])))
		}
	}))
	defer srv.Close()
	cases := []struct {
		cmd  Command
		body string
	}{
		0: {
			&headerCmd{&Cmd{U: mustParse(t, srv.URL+"/a"), M: "GET"},
				http.Header{"A": {"a"}}},
			"A:a\n",
		},
		1: {
			&Cmd{U: mustParse(t, srv.URL+"/b"), M: "GET"},
			"",
		},
		2: {
			&headerCmd{&Cmd{U: mustParse(t, srv.URL+"/c"), M: "GET"},
				http.Header{"C": {"c"}, "D": {"d"}}},
			"C:c\nD:d\n",
		},
	}
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for i, c := range cases {
		if err := q.Send(c.cmd); err != nil {
			t.Errorf("%d: error sending command: %s", i, err)
		}
	}
	q.Close()
	var urls []string
	for i, c := range cases {
		urls = append(urls, c.cmd.URL().String())
		if b := sh.BodyFor(c.cmd.URL().String()); b != c.body {
			t.Errorf("%d: expected body '%s', got '%s'", i, c.body, b)
		}
	}
	if !sh.CalledWithExactly(urls...) {
		t.Error("expected handler to be called for all cases")
	}
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no error, got %d", cnt)
	}
}

type fullCmd struct {
	*Cmd
	user, pwd string
	r         io.Reader
	vals      url.Values
	cooks     []*http.Cookie
	hdr       http.Header
}

func (f *fullCmd) BasicAuth() (string, string) {
	return f.user, f.pwd
}

func (f *fullCmd) Reader() io.Reader {
	return f.r
}

func (f *fullCmd) Values() url.Values {
	return f.vals
}

func (f *fullCmd) Cookies() []*http.Cookie {
	return f.cooks
}

func (f *fullCmd) Header() http.Header {
	return f.hdr
}

func TestFullCmd(t *testing.T) {
	creds := base64.StdEncoding.EncodeToString([]byte("me:you"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Basic auth
		auth := req.Header.Get("Authorization")
		if auth != "Basic "+creds {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"Authorization Required\"")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Cookies
		for i, c := range req.Cookies() {
			if i > 0 {
				w.Write([]byte{'&'})
			}
			w.Write([]byte(c.Name))
		}
		// Header
		for k, v := range req.Header {
			if len(k) == 1 {
				w.Write([]byte(fmt.Sprintf("%s:%s\n", k, v[0])))
			}
		}
		// Body
		b, err := ioutil.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}))
	defer srv.Close()

	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	cmd := &fullCmd{
		&Cmd{U: mustParse(t, srv.URL+"/a"), M: "POST"},
		"me", "you",
		strings.NewReader("body"),
		url.Values{"ignored": {"val"}},
		[]*http.Cookie{&http.Cookie{Name: "a"}},
		http.Header{"A": {"a"}},
	}
	if err := q.Send(cmd); err != nil {
		t.Fatal(err)
	}
	q.Close()
	// Assert 200 status
	if st := sh.StatusFor(cmd.URL().String()); st != 200 {
		t.Errorf("expected status %d, got %d", 200, st)
	}
	// Assert body (Cookies + Header)
	exp := "aA:a\nbody"
	if b := sh.BodyFor(cmd.URL().String()); b != exp {
		t.Errorf("expected body '%s', got '%s'", exp, b)
	}
}

func TestHandlerCmd(t *testing.T) {
	var result int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	defer srv.Close()

	cases := []struct {
		cmd  Command
		want int32
	}{
		0: {
			mustCmd(NewHandlerCmd("GET", srv.URL+"/a", func(ctx *Context, res *http.Response, err error) {
				atomic.AddInt32(&result, 1)
			})), 1,
		},
		1: {
			&Cmd{U: mustParse(t, srv.URL+"/b"), M: "GET"}, -1,
		},
	}

	f := New(HandlerFunc(func(ctx *Context, res *http.Response, err error) {
		atomic.AddInt32(&result, -1)
	}))
	f.CrawlDelay = 0

	for i, c := range cases {
		result = 0
		q := f.Start()
		if err := q.Send(c.cmd); err != nil {
			t.Errorf("%d: error sending command: %s", i, err)
		}
		q.Close()

		if result != c.want {
			t.Errorf("%d: want %d, got %d", i, c.want, result)
		}
	}
}

func mustCmd(cmd Command, err error) Command {
	if err != nil {
		panic(err)
	}
	return cmd
}

func mustParse(t *testing.T, raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
