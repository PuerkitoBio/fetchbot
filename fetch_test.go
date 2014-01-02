package fetch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type spyHandler struct {
	mu   sync.Mutex
	cmds []Command
	errs []error
}

func (sh *spyHandler) Handle(cmd Command, res *http.Response, req *http.Request, err error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.cmds = append(sh.cmds, cmd)
	sh.errs = append(sh.errs, err)
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

var nopHandler Handler = HandlerFunc(func(cmd Command, res *http.Response, req *http.Request, err error) {})

// Test that an initialized Fetcher has the right defaults.
func TestNew(t *testing.T) {
	f := New(nopHandler, -1)
	if f.CrawlDelay != DefaultCrawlDelay {
		t.Errorf("expected CrawlDelay to be %s, got %s", DefaultCrawlDelay, f.CrawlDelay)
	}
	if f.HttpClient != http.DefaultClient {
		t.Errorf("expected HttpClient to be %p (default net/http client), got %p", http.DefaultClient, f.HttpClient)
	}
	if f.UserAgent != DefaultUserAgent {
		t.Errorf("expected UserAgent to be %s, got %s", DefaultUserAgent, f.UserAgent)
	}
	if f.WorkerIdleTTL != DefaultWorkerIdleTTL {
		t.Errorf("expected WorkerIdleTTL to be %s, got %s", DefaultWorkerIdleTTL, f.WorkerIdleTTL)
	}
	if f.buf != DefaultChanBufferSize {
		t.Errorf("expected chan buffer size to be %d, got %d", DefaultChanBufferSize, f.buf)
	}
}

func TestEnqueueUnstarted(t *testing.T) {
	f := New(nopHandler, -1)
	_, err := f.EnqueueGet("http://localhost")
	if err != ErrUnstarted {
		t.Errorf("EnqueueGet: expected error %s, got %v", ErrUnstarted, err)
	}
	_, err = f.EnqueueHead("http://localhost")
	if err != ErrUnstarted {
		t.Errorf("EnqueueHead: expected error %s, got %v", ErrUnstarted, err)
	}
	err = f.EnqueueString("http://localhost", "PUT")
	if err != ErrUnstarted {
		t.Errorf("EnqueueString: expected error %s, got %v", ErrUnstarted, err)
	}
}

func TestEnqueueVariadic(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b", "/nohost", ":"}
	handled := cases[:len(cases)-1]

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh, 0)
	f.CrawlDelay = 0
	f.Start()
	n, err := f.EnqueueGet(cases...)
	if n != len(handled) {
		t.Errorf("expected %d URLs enqueued, got %d", len(handled), n)
	}
	if _, ok := err.(*url.Error); !ok {
		t.Errorf("expected parse error, got %v", err)
	}
	// Stop to wait for all commands to be processed
	f.Stop()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(handled...); !ok {
		t.Error("expected handler to be called with valid cases")
	}
	// Expect 1 error for empty host
	if cnt := sh.Errors(); cnt != 1 {
		t.Errorf("expected 1 error, got %d", cnt)
	}
	// Assert that the empty host error is actually that error
	if err := sh.ErrorFor(handled[len(handled)-1]); err != ErrEmptyHost {
		t.Errorf("expected error %s for url '%s', got %v", ErrEmptyHost, handled[len(handled)-1], err)
	}
}

func TestEnqueueString(t *testing.T) {
	// Start a test server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Define the raw URLs to enqueue
	cases := []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"}

	// Start the Fetcher
	sh := &spyHandler{}
	f := New(sh, 0)
	f.CrawlDelay = 0
	f.Start()
	for _, c := range cases {
		err := f.EnqueueString(c, "GET")
		if err != nil {
			t.Fatal(err)
		}
	}
	// Stop to wait for all commands to be processed
	f.Stop()
	// Assert that the handler got called with the right values
	if ok := sh.CalledWithExactly(cases...); !ok {
		t.Error("expected handler to be called with all cases")
	}
	if cnt := sh.Errors(); cnt > 0 {
		t.Errorf("expected no errors, got %d", cnt)
	}
}

// TODO : ErrDisallowed, Idle hosts, prove deadlock error possibility before fixing it
