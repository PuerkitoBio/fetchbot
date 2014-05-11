// Copyright 2014 Martin Angers and Contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fetchbot

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt.go"
)

var (
	// A Command cannot be enqueued if it has an URL with an empty host.
	ErrEmptyHost = errors.New("fetchbot: invalid empty host")

	// Error when the requested URL is disallowed by the robots.txt policy.
	ErrDisallowed = errors.New("fetchbot: disallowed by robots.txt")

	// Error when a Send call is made on a closed Queue.
	ErrQueueClosed = errors.New("fetchbot: send on a closed queue")
)

const (
	// The default crawl delay to use if there is no robots.txt specified delay.
	DefaultCrawlDelay = 5 * time.Second
	// The default user agent string.
	DefaultUserAgent = "Fetchbot (https://github.com/PuerkitoBio/fetchbot)"
	// The default time-to-live of an idle host worker goroutine. If no URL is sent
	// for a given host within this duration, this host's goroutine is disposed of.
	DefaultWorkerIdleTTL = 30 * time.Second
)

type CrawlConfig struct {
	// Default delay to use between requests to a same host if there is no robots.txt
	// crawl delay.
	CrawlDelay time.Duration

	// The *http.Client to use for the requests. If nil, defaults to the net/http
	// package's default client.
	HttpClient *http.Client

	// The user-agent string to use for robots.txt validation and URL fetching.
	UserAgent string
}

var DefaultCrawlConfig = CrawlConfig{
	CrawlDelay: DefaultCrawlDelay,
	HttpClient: http.DefaultClient,
	UserAgent: DefaultUserAgent,
}

// A Fetcher defines the parameters for running a web crawler.
type Fetcher struct {
	CrawlConfig

	// The Handler to be called for each request. All successfully enqueued requests
	// produce a Handler call.
	Handler Handler

	// The time a host-dedicated worker goroutine can stay idle, with no Command to enqueue,
	// before it is stopped and cleared from memory.
	WorkerIdleTTL time.Duration

	// q holds the Queue to send data to the fetcher and optionnaly close (stop) it.
	q *Queue
	// dbg is a channel used to push debug information.
	dbg chan *DebugInfo

	// The next three maps and list are always accessed from the same single goroutine,
	// (processQueue) so no sync is required.

	// hosts maps the host names to its dedicated requests channel.
	hosts map[string]*hostFetcherInfiniteQ
	// hostToIdleElem keeps an O(1) access from a host to its corresponding element in the
	// idle list.
	hostToIdleElem map[string]*list.Element
	// idleList keeps a sorted list of hosts in order of last access, so that removing idle
	// workers is a quick and easy process.
	idleList *list.List
}

// UnsafeHostFetcher receives commands on CmdIn that all pertain to the host in
// BaseURL.  It executes them using HttpClient, and submits errors and responses
// to CmdHandler.  Use HostFetcher instead of UnsafeHostFetcher unless you know
// it is safe to disregard robots.txt.
type UnsafeHostFetcher struct {
	BaseURL *url.URL
	CmdHandler
	CmdIn chan Command
	HttpClient *http.Client
	UserAgent string
	RateThrottler
}

// HostFetcher is an UnsafeHostFetcher that supports robots.txt.
type HostFetcher struct {
	*UnsafeHostFetcher
	agent *robotstxt.Group
}

// hostFetcherInfiniteQ marries a HostFetcher and an infinite queue.
type hostFetcherInfiniteQ struct {
	HostFetcher
	InfCmdIn chan Command
}

// The DebugInfo holds information to introspect the Fetcher's state.
type DebugInfo struct {
	NumHosts int
}

// The hostTimestamp holds the host and last access timestamp used to free idle
// hosts' goroutines.
type hostTimestamp struct {
	host string
	ts   time.Time
}

// New returns an initialized Fetcher.
func New(h Handler) *Fetcher {
	return &Fetcher{
		CrawlConfig:   DefaultCrawlConfig,
		Handler:       h,
		WorkerIdleTTL: DefaultWorkerIdleTTL,
		dbg:           make(chan *DebugInfo, 1),
	}
}

// Queue offers methods to send Commands to the Fetcher, and to Stop the crawling process.
// It is safe to use from concurrent goroutines.
type Queue struct {
	ch     chan Command
	closed chan struct{}
	wg     sync.WaitGroup
}

// Close closes the Queue so that no more Commands can be sent. It blocks until
// the Fetcher drains all pending commands. After the call, the Fetcher is stopped.
func (q *Queue) Close() error {
	// Make sure it is not already closed, as this is a run-time panic
	select {
	case <-q.closed:
		// Already closed, no-op
		return nil
	default:
		// Close the signal-channel
		close(q.closed)
		// Send a nil Command to make sure the processQueue method sees the close signal.
		q.ch <- nil
		// Wait for the Fetcher to drain.
		q.wg.Wait()
		return nil
	}
}

// Block blocks the current goroutine until the Queue is closed.
func (q *Queue) Block() {
	<-q.closed
}

// Send enqueues a Command into the Fetcher. If the Queue has been closed, it
// returns ErrQueueClosed.
func (q *Queue) Send(c Command) error {
	select {
	case <-q.closed:
		return ErrQueueClosed
	default:
		q.ch <- c
	}
	return nil
}

// SendString enqueues a method and some URL strings into the Fetcher. It returns an error
// if the URL string cannot be parsed, or if the Queue has been closed.
// The first return value is the number of URLs successfully enqueued.
func (q *Queue) SendString(method string, rawurl ...string) (int, error) {
	return q.sendWithMethod(method, rawurl)
}

// SendStringHead enqueues the URL strings to be fetched with a HEAD method.
// It returns an error if the URL string cannot be parsed, or if the Queue has been closed.
// The first return value is the number of URLs successfully enqueued.
func (q *Queue) SendStringHead(rawurl ...string) (int, error) {
	return q.sendWithMethod("HEAD", rawurl)
}

// SendStringGet enqueues the URL strings to be fetched with a GET method.
// It returns an error if the URL string cannot be parsed, or if the Queue has been closed.
// The first return value is the number of URLs successfully enqueued.
func (q *Queue) SendStringGet(rawurl ...string) (int, error) {
	return q.sendWithMethod("GET", rawurl)
}

// Parses the URL strings and enqueues them as *Cmd. It returns the number of URLs
// successfully enqueued, and an error if the URL string cannot be parsed or
// the Queue has been closed.
func (q *Queue) sendWithMethod(method string, rawurl []string) (int, error) {
	for i, v := range rawurl {
		parsed, err := url.Parse(v)
		if err != nil {
			return i, err
		}
		if err := q.Send(&Cmd{U: parsed, M: method}); err != nil {
			return i, err
		}
	}
	return len(rawurl), nil
}

// Start the Fetcher, and returns the Queue to use to send Commands to be fetched.
func (f *Fetcher) Start() *Queue {
	// Create the internal maps and lists
	f.hosts = make(map[string]*hostFetcherInfiniteQ)
	f.hostToIdleElem = make(map[string]*list.Element)
	f.idleList = list.New()
	// Create the Queue
	f.q = &Queue{
		ch:     make(chan Command, 1),
		closed: make(chan struct{}),
	}
	// Start the one and only queue processing goroutine.
	f.q.wg.Add(1)
	go f.processQueue()
	// Return the Queue struct used to send more Commands and optionally close
	// the queue and stop the fetcher.
	return f.q
}

// Debug returns the channel to use to receive the debugging information. It is not intended
// to be used by package users.
func (f *Fetcher) Debug() <-chan *DebugInfo {
	return f.dbg
}

// processQueue runs the queue in its own goroutine. This is the only goroutine
// that should access the f.hosts, f.hostToIdleElem and f.idleList fields.
func (f *Fetcher) processQueue() {
loop:
	for v := range f.q.ch {
		if v == nil {
			// Special case, when the Queue is closed, a nil command is sent, use this
			// indicator to check for the closed signal, instead of looking on every loop.
			select {
			case <-f.q.closed:
				// Close signal, exit loop
				break loop
			default:
				// Keep going
			}
		}
		if err := f.handleCommand(v); err != nil {
			// Handle on a separate goroutine, the Queue goroutine must not block.
			go f.Handler.Handle(&Context{Cmd: v, Q: f.q}, nil, ErrEmptyHost)
		}
	}
	// Close all host channels now that it is impossible to send on those. Those are the `in`
	// channels of the infinite queue. It will then drain any pending events, triggering the
	// handlers for each in the worker goro, and then the infinite queue goro will terminate
	// and close the `out` channel, which in turn will terminate the worker goro.
	for _, hf := range f.hosts {
		close(hf.InfCmdIn)
	}
	f.q.wg.Done()
}

func (f *Fetcher) handleCommand(v Command) error {
	u := v.URL()
	// Check if a channel is already started for this host
	hf, ok := f.hosts[u.Host]
	if !ok {
		// Start a new channel and goroutine for this host.
		newhf, err := f.newHostFetcher(u)
		if err != nil {
			return err
		}
		f.hosts[u.Host] = newhf
		hf = newhf
	}
	// Send the request
	hf.InfCmdIn <- v
	// Adjust the timestamp for this host's idle TTL
	f.setHostTimestamp(u.Host)
	// Garbage collect idle hosts
	f.freeIdleHosts()
	// Send debug info, but do not block if full
	select {
	case f.dbg <- &DebugInfo{len(f.hosts)}:
	default:
	}
	return nil
}

func (f *Fetcher) newHostFetcher(u *url.URL) (*hostFetcherInfiniteQ, error) {
	if u.Host == "" {
		// The URL must be rooted with a host. 
		return nil, ErrEmptyHost
	}
	baseurl, err := u.Parse("/")
	if err != nil {
		return nil, err
	}

	// Create the infinite queue: the in channel to send on, and the out channel
	// to read from in the host's goroutine, and add to the hosts map
	var out chan Command
	in, out := make(chan Command, 1), make(chan Command, 1)
	chand := CmdHandlerFunc(func(cmd Command, res *http.Response, err error) {
		f.Handler.Handle(&Context{Cmd: cmd, Q: f.q}, res, err)
	})
	hf := NewHostFetcher(f.CrawlConfig, baseurl, chand, out)
	// Start the infinite queue goroutine for this host
	go sliceIQ(in, out)
	// Start the working goroutine for this host
	f.q.wg.Add(1)
	go func() {
		hf.Run()
		f.q.wg.Done()
	}()
	return &hostFetcherInfiniteQ{*hf, in}, nil
}

// Add the host to the idle list, along with its last access timestamp. The idle
// list is maintained in sorted order so that the LRU host is first.
func (f *Fetcher) setHostTimestamp(host string) {
	if e, ok := f.hostToIdleElem[host]; !ok {
		e = f.idleList.PushBack(&hostTimestamp{host, time.Now()})
		f.hostToIdleElem[host] = e
	} else {
		e.Value.(*hostTimestamp).ts = time.Now()
		f.idleList.MoveToBack(e)
	}
}

// Free the hosts that have been idle for at least Fetcher.WorkerIdleTTL.
func (f *Fetcher) freeIdleHosts() {
	for e := f.idleList.Front(); e != nil; {
		hostts := e.Value.(*hostTimestamp)
		if time.Now().Sub(hostts.ts) > f.WorkerIdleTTL {
			hf := f.hosts[hostts.host]
			// Remove and close this host's channel
			delete(f.hosts, hostts.host)
			close(hf.CmdIn)
			// Remove from idle list
			delete(f.hostToIdleElem, hostts.host)
			newe := e.Next()
			f.idleList.Remove(e)
			// Continue with next element, there may be more to free
			e = newe
		} else {
			// The list is ordered by oldest first, so as soon as one host is not passed
			// its TTL, we can safely exit the loop.
			break
		}
	}
}

// NewUnsafeHostFetcher creates a new UnsafeHostFetcher.
func NewUnsafeHostFetcher(c CrawlConfig, baseurl *url.URL, chand CmdHandler, cmd chan Command) *UnsafeHostFetcher {
	return &UnsafeHostFetcher{
		HttpClient: c.HttpClient,
		UserAgent: c.UserAgent,
		BaseURL: baseurl,
		CmdHandler: chand,
		CmdIn: cmd,
		RateThrottler: RateThrottler{Rate: c.CrawlDelay},
	}
}

// Run reads and execute commands from CmdIn until it is closed.
func (uhf *UnsafeHostFetcher) Run() {
	for v := range uhf.CmdIn {
		uhf.DoCommand(v)
	}
}

// DoCommand executes a single command.  Normally only used by Run, but nothing
// prevents users of UnsafeHostFetcher from giving a nil CmdIn can invoking
// DoCommand themselves.  DoCommand will respect the throttling specified in
// the UnsafeHostFetcher's RateThrottler, which is initialized based on the
// CrawlDelay used in building it.
func (uhf *UnsafeHostFetcher) DoCommand(cmd Command) {
	uhf.ActMaybeWait()
	res, err := uhf.doRequest(cmd)
	uhf.visit(cmd, res, err)
}

// Call the Handler for this Command. Closes the response's body.
func (uhf *UnsafeHostFetcher) visit(cmd Command, res *http.Response, err error) {
	if res != nil {
		defer res.Body.Close()
	}
	uhf.CmdHandler.HandleCmd(cmd, res, err)
}

// Prepare and execute the request for this Command.
func (uhf *UnsafeHostFetcher) doRequest(r Command) (*http.Response, error) {
	if r.URL().Host != uhf.BaseURL.Host {
		return nil, fmt.Errorf("received Command for wrong host '%s'",
		                       r.URL().Host)
	}
	req, err := http.NewRequest(r.Method(), r.URL().String(), nil)
	if err != nil {
		return nil, err
	}
	// If the Command implements some other recognized interfaces, set
	// the request accordingly (see cmd.go for the list of interfaces).
	// First, the Header values.
	if hd, ok := r.(HeaderProvider); ok {
		for k, v := range hd.Header() {
			req.Header[k] = v
		}
	}
	// BasicAuth has higher priority than an Authorization header set by
	// a HeaderProvider.
	if ba, ok := r.(BasicAuthProvider); ok {
		req.SetBasicAuth(ba.BasicAuth())
	}
	// Cookies are added to the request, even if some cookies were set
	// by a HeaderProvider.
	if ck, ok := r.(CookiesProvider); ok {
		for _, c := range ck.Cookies() {
			req.AddCookie(c)
		}
	}
	// For the body of the request, ReaderProvider has higher priority
	// than ValuesProvider.
	if rd, ok := r.(ReaderProvider); ok {
		rdr := rd.Reader()
		rc, ok := rdr.(io.ReadCloser)
		if !ok {
			rc = ioutil.NopCloser(rdr)
		}
		req.Body = rc
	} else if val, ok := r.(ValuesProvider); ok {
		v := val.Values()
		req.Body = ioutil.NopCloser(strings.NewReader(v.Encode()))
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	// If there was no User-Agent implicitly set by the HeaderProvider,
	// set it to the default value.
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", uhf.UserAgent)
	}
	// Do the request.
	res, err := uhf.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// NewHostFetcher creates a new HostFetcher.
func NewHostFetcher(c CrawlConfig, baseurl *url.URL, chand CmdHandler, cmd chan Command) *HostFetcher {
	return &HostFetcher{UnsafeHostFetcher: NewUnsafeHostFetcher(c, baseurl, chand, cmd)}
}

// Run fetches robots.txt, then runs continuously until the command channel is closed,
// executing the commands sent to it.
func (hf *HostFetcher) Run() {
	hf.fetchRobotsTxt()
	if hf.agent != nil && hf.agent.CrawlDelay > 0 {
		hf.Rate = hf.agent.CrawlDelay
	}

	for v := range hf.CmdIn {
		hf.doSafeCommand(v)
	}
}

// doSafeCommand executes a single command, unless robots.txt forbids it, in which
// case ErrDisallowed is passed to the handler.
func (hf *HostFetcher) doSafeCommand(cmd Command) {
	if hf.agent == nil || hf.agent.Test(cmd.URL().Path) {
		hf.UnsafeHostFetcher.DoCommand(cmd)
		// Path allowed, process the request
	} else {
		// Path disallowed by robots.txt
		hf.visit(cmd, nil, ErrDisallowed)
	}
}

// Get the robots.txt User-Agent-specific group.
func (hf *HostFetcher) fetchRobotsTxt() {
	rob, err := hf.BaseURL.Parse("/robots.txt")
	if err != nil {
		// This shouldn't be possible:
		panic("unable to parse /robots.txt")
	}

	res, err := hf.doRequest(robotCommand{&Cmd{U: rob, M: "GET"}})
	if err != nil {
		// TODO: Ignore robots.txt request error?
		fmt.Fprintf(os.Stderr, "fetchbot: error fetching robots.txt: %s\n", err)
		return
	}
	defer res.Body.Close()
	robData, err := robotstxt.FromResponse(res)
	if err != nil {
		// TODO : Ignore robots.txt parse error?
		fmt.Fprintf(os.Stderr, "fetchbot: error parsing robots.txt: %s\n", err)
		return
	}
	hf.agent = robData.FindGroup(hf.UserAgent)
	hf.LastAct = time.Now()
}

// RateThrottler records when an action is done, and how frequently it is allowed.
type RateThrottler struct {
	Rate time.Duration
	LastAct time.Time
}

// Sleep until next action, then note the time at which it is performed.
func (t *RateThrottler) ActMaybeWait() {
	if t.Rate == 0 {
		return
	}

	elapsed := time.Now().Sub(t.LastAct)
	if elapsed < t.Rate {
		time.Sleep(t.Rate - elapsed)
	}
	t.LastAct = time.Now()
}

