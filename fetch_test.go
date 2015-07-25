// Copyright 2014 Martin Angers and Contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fetchbot

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

type spyHandler struct {
	mu     sync.Mutex
	cmds   []Command
	errs   []error
	res    []*http.Response
	bodies []string
	fn     Handler
}

func (sh *spyHandler) Handle(ctx *Context, res *http.Response, err error) {
	sh.mu.Lock()
	sh.cmds = append(sh.cmds, ctx.Cmd)
	sh.errs = append(sh.errs, err)
	sh.res = append(sh.res, res)
	if res == nil {
		sh.bodies = append(sh.bodies, "")
	} else {
		b, err := ioutil.ReadAll(res.Body)
		if err != nil {
			sh.bodies = append(sh.bodies, "")
		}
		sh.bodies = append(sh.bodies, string(b))
	}
	sh.mu.Unlock()
	if sh.fn != nil {
		sh.fn.Handle(ctx, res, err)
	}
}

func (sh *spyHandler) Errors() int {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	cnt := 0
	for _, e := range sh.errs {
		if e != nil {
			cnt++
		}
	}
	return cnt
}

func (sh *spyHandler) CommandFor(rawurl string) Command {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for _, c := range sh.cmds {
		if c.URL().String() == rawurl {
			return c
		}
	}
	return nil
}

func (sh *spyHandler) ErrorFor(rawurl string) error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ix := -1
	for i, c := range sh.cmds {
		if c.URL().String() == rawurl {
			ix = i
			break
		}
	}
	if ix >= 0 {
		return sh.errs[ix]
	}
	return nil
}

func (sh *spyHandler) StatusFor(rawurl string) int {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ix := -1
	for i, c := range sh.cmds {
		if c.URL().String() == rawurl {
			ix = i
			break
		}
	}
	if ix >= 0 && sh.res[ix] != nil {
		return sh.res[ix].StatusCode
	}
	return -1
}

func (sh *spyHandler) BodyFor(rawurl string) string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	ix := -1
	for i, c := range sh.cmds {
		if c.URL().String() == rawurl {
			ix = i
			break
		}
	}
	if ix >= 0 {
		return sh.bodies[ix]
	}
	return ""
}

func (sh *spyHandler) CalledWithExactly(rawurl ...string) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(sh.cmds) != len(rawurl) {
		return false
	}
	for _, u := range rawurl {
		ok := false
		for _, c := range sh.cmds {
			if u == c.URL().String() {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

var nopHandler = HandlerFunc(func(ctx *Context, res *http.Response, err error) {})

// Test that an initialized Fetcher has the right defaults.
func TestNew(t *testing.T) {
	f := New(nopHandler)
	if f.CrawlDelay != DefaultCrawlDelay {
		t.Errorf("expected CrawlDelay to be %s, got %s", DefaultCrawlDelay, f.CrawlDelay)
	}
	if f.HttpClient != http.DefaultClient {
		t.Errorf("expected HttpClient to be %v (default net/http client), got %v", http.DefaultClient, f.HttpClient)
	}
	if f.UserAgent != DefaultUserAgent {
		t.Errorf("expected UserAgent to be %s, got %s", DefaultUserAgent, f.UserAgent)
	}
	if f.WorkerIdleTTL != DefaultWorkerIdleTTL {
		t.Errorf("expected WorkerIdleTTL to be %s, got %s", DefaultWorkerIdleTTL, f.WorkerIdleTTL)
	}
}

func TestQueueClosed(t *testing.T) {
	f := New(nil)
	q := f.Start()
	q.Close()
	_, err := q.SendStringGet("http://host/a")
	if err != ErrQueueClosed {
		t.Errorf("expected error %s, got %v", ErrQueueClosed, err)
	}
	// Test that closing a closed Queue doesn't panic
	q.Close()
}

func TestBlock(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	_, err := q.SendStringGet(cases...)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	ok := false
	go func() {
		q.Block()
		mu.Lock()
		ok = true
		mu.Unlock()
	}()
	time.Sleep(100 * time.Millisecond)
	q.Close()
	time.Sleep(100 * time.Millisecond)
	// Assert that the handler got called with all cases
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Expect 0 error
	if cnt := sh.Errors(); cnt != 0 {
		t.Errorf("expected no error, got %d", cnt)
	}
	// Expect ok to be true
	mu.Lock()
	if !ok {
		t.Error("expected flag to be set to true after Block release, got false")
	}
	mu.Unlock()
}

func TestSendVariadic(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b", "/nohost", ":"}
	handled := cases[:len(cases)-2]

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	n, err := q.SendStringGet(cases...)
	if n != 2 {
		t.Errorf("expected %d URLs enqueued, got %d", 2, n)
	}
	if err != ErrEmptyHost {
		t.Errorf("expected %v, got %v", ErrEmptyHost, err)
	}
	// Stop to wait for all commands to be processed
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(handled...); !ok {
		t.Error("expected handler to be called with valid cases")
	}
	// Expect no error
	if cnt := sh.Errors(); cnt != 0 {
		t.Errorf("expected no error, got %d", cnt)
	}
}

func TestUserAgent(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a"}

	// Start the Fetcher
	f := New(nil)
	sh := &spyHandler{fn: HandlerFunc(func(ctx *Context, res *http.Response, err error) {
		if f.UserAgent != res.Request.UserAgent() {
			t.Errorf("expected user agent %s, got %s", f.UserAgent, res.Request.UserAgent())
		}
	})}
	f.Handler = sh
	f.CrawlDelay = 0
	f.UserAgent = "test"
	q := f.Start()
	q.SendStringGet(cases...)
	// Stop to wait for all commands to be processed
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}

func TestSendString(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for _, c := range cases {
		_, err := q.SendString("GET", c)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Stop to wait for all commands to be processed
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}

func TestFetchDisallowed(t *testing.T) {
	// Start 2 test servers
	srvDisAll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`
User-agent: *
Disallow: /
`))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srvDisAll.Close()
	srvAllSome := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`
User-agent: Googlebot
Disallow: /

User-agent: Fetchbot
Disallow: /a
`))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srvAllSome.Close()

	// Define the raw URLs to enqueue
	cases := []string{srvDisAll.URL + "/a", srvDisAll.URL + "/b", srvAllSome.URL + "/a", srvAllSome.URL + "/b"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for _, c := range cases {
		_, err := q.SendString("GET", c)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Stop to wait for all commands to be processed
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was the correct number of expected errors
	if cnt := sh.Errors(); cnt != 3 {
		t.Errorf("expected 3 errors, got %d", cnt)
	}
	for i := 0; i < 3; i++ {
		if err := sh.ErrorFor(cases[i]); err != ErrDisallowed {
			t.Errorf("expected error %s for %s, got %v", ErrDisallowed, cases[i], err)
		}
	}
}

func TestCrawlDelay(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`
User-agent: Fetchbot
Crawl-delay: 1
`))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	start := time.Now()
	q := f.Start()
	_, err := q.SendStringGet(cases...)
	if err != nil {
		t.Fatal(err)
	}
	// Stop to wait for all commands to be processed
	q.Close()
	delay := time.Now().Sub(start)
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
	// Assert that the total elapsed time is around 2 seconds
	if delay < 2*time.Second || delay > (2*time.Second+100*time.Millisecond) {
		t.Errorf("expected delay to be around 2s, got %s", delay)
	}
}

func TestManyCrawlDelays(t *testing.T) {
	// Skip if -short flag is set
	if testing.Short() {
		t.SkipNow()
	}
	// Start two test servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`
User-agent: Fetchbot
Crawl-delay: 1
`))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv2.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv1.URL + "/a", srv1.URL + "/b", srv2.URL + "/a", srv2.URL + "/b"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 2 * time.Second
	start := time.Now()
	q := f.Start()
	_, err := q.SendStringGet(cases...)
	if err != nil {
		t.Fatal(err)
	}
	// Stop to wait for all commands to be processed
	q.Close()
	delay := time.Now().Sub(start)
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
	// Assert that the total elapsed time is around 4 seconds
	if delay < 4*time.Second || delay > (4*time.Second+100*time.Millisecond) {
		t.Errorf("expected delay to be around 4s, got %s", delay)
	}
}

// Custom Command for TestCustomCommand
type IDCmd struct {
	*Cmd
	ID int
}

func TestCustomCommand(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	for i, c := range cases {
		parsed, err := url.Parse(c)
		if err != nil {
			t.Fatal(err)
		}
		q.Send(&IDCmd{&Cmd{U: parsed, M: "GET"}, i})
	}
	// Stop to wait for all commands to be processed
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
	// Assert that all commands got passed with the correct custom information
	for i, c := range cases {
		cmd := sh.CommandFor(c)
		if idc, ok := cmd.(*IDCmd); !ok {
			t.Errorf("expected command for %s to be an *IDCmd, got %T", c, cmd)
		} else if idc.ID != i {
			t.Errorf("expected command ID for %s to be %d, got %d", c, i, idc.ID)
		}
	}
}

func TestFreeIdleHost(t *testing.T) {
	// Start 2 test servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv2.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv1.URL + "/a", srv2.URL + "/a"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	f.WorkerIdleTTL = 100 * time.Millisecond
	q := f.Start()
	for i, c := range cases {
		if i == 1 {
			// srv1 should now be removed
			f.mu.Lock()
			if _, ok := f.hosts[srv1.URL[len("http://"):]]; ok {
				t.Error("expected server srv1 to be removed from hosts")
			}
			f.mu.Unlock()
		}
		_, err := q.SendStringGet(c)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(110 * time.Millisecond)
	}
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}

func TestRemoveHosts(t *testing.T) {
	// Start 2 test servers
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv2.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv1.URL + "/a", srv2.URL + "/a"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh)
	f.CrawlDelay = 0
	f.WorkerIdleTTL = 100 * time.Millisecond
	q := f.Start()
	for _, c := range cases {
		_, err := q.SendStringGet(c)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(101 * time.Millisecond)
	}
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
	// Assert that hosts are all removed
	if l := len(f.hosts); l > 0 {
		t.Errorf("expected hosts to be empty, got %d", l)
	}
}

func TestRestart(t *testing.T) {
	f := New(nil)
	f.CrawlDelay = 0
	for i := 0; i < 2; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}))
		cases := []string{srv.URL + "/a", srv.URL + "/b"}
		sh := &spyHandler{}
		f.Handler = sh
		q := f.Start()
		// Assert that the lists and maps are empty
		if len(f.hosts) != 0 {
			t.Errorf("run %d: expected clean slate after call to Start, found hosts=%d", i, len(f.hosts))
		}
		_, err := q.SendStringGet(cases...)
		if err != nil {
			t.Fatal(err)
		}
		q.Close()
		// Assert that the handler got called with the right values
		if ok := sh.CalledWithExactly(cases...); !ok {
			t.Error("expected handler to be called with all cases")
		}
		// Assert that there was no error
		if cnt := sh.Errors(); cnt > 0 {
			t.Errorf("expected no errors, got %d", cnt)
		}
		srv.Close()
	}
}

func TestOverflowBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	cases := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c", srv.URL + "/d", srv.URL + "/e", srv.URL + "/f"}
	signal := make(chan struct{})
	sh := &spyHandler{fn: HandlerFunc(func(ctx *Context, res *http.Response, err error) {
		if ctx.Cmd.URL().Path == "/a" {
			// Enqueue a bunch, while this host's goroutine is busy waiting for this call
			_, err := ctx.Q.SendStringGet(cases[1:]...)
			if err != nil {
				t.Fatal(err)
			}
			close(signal)
		}
	})}
	f := New(sh)
	f.CrawlDelay = 0
	q := f.Start()
	_, err := q.SendStringGet(cases[0])
	if err != nil {
		t.Fatal(err)
	}
	<-signal
	q.Close()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}

func TestCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	allowHandler := make(chan struct{})
	allowCancel := make(chan struct{})
	sh := &spyHandler{fn: HandlerFunc(func(ctx *Context, res *http.Response, err error) {
		// allow cancel as soon as /0 is received
		<-allowHandler
		if res.Request.URL.Path == "/0" {
			close(allowCancel)
		}
	})}

	f := New(sh)
	f.CrawlDelay = time.Second
	f.DisablePoliteness = true
	q := f.Start()
	// enqueue a bunch of URLs
	for i := 0; i < 1000; i++ {
		_, err := q.SendStringGet(srv.URL + "/" + strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	// allow one to proceed
	close(allowHandler)
	// wait for cancel signal
	<-allowCancel
	q.Cancel()

	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(srv.URL + "/0"); !ok {
		t.Error("expected handler to be called only with /0")
	}
	// Assert that there was no error
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}
