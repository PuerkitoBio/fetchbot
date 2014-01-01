package fetch

import (
	"container/list"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/temoto/robotstxt.go"
)

var (
	ErrEmptyHost  = errors.New("fetch: invalid empty host")
	ErrDisallowed = errors.New("fetch: disallowed by robots.txt")
)

const (
	DefaultCrawlDelay     = 5 * time.Second
	DefaultChanBufferSize = 10
	DefaultUserAgent      = "fetchbot (https://github.com/PuerkitoBio/fetch)"
	DefaultWorkerIdleTTL  = 30 * time.Second
)

type Fetcher struct {
	// The Handler to be called for each request. It is guaranteed that all requests
	// successfully enqueued will produce a Handler call.
	Handler Handler

	// Default delay to use between requests if there is no robots.txt crawl delay.
	CrawlDelay time.Duration

	// The HttpClient to use for the requests. If nil, defaults to the net/http
	// package's default client.
	HttpClient *http.Client

	// Defines the user-agent string to use for robots.txt validation and URL fetching.
	UserAgent string

	// The time a host-dedicated worker goroutine can stay idle, with no Command to fetch,
	// before it is stopped and cleared from memory.
	WorkerIdleTTL time.Duration

	// ch is the channel to enqueue requests for this fetcher.
	ch chan Command
	// buf is the buffer capacity of the channel
	buf int
	// wg waits for the processQueue func and worker goroutines to finish.
	wg sync.WaitGroup

	// hosts maps the host names to its dedicated requests channel.
	hosts map[string]chan Command
	// hostToIdleElem keeps an O(1) access from a host to its corresponding element in the
	// idle list.
	hostToIdleElem map[string]*list.Element
	// idleList keeps a sorted list of hosts in order of last access, so that removing idle
	// workers is a quick and easy process.
	idleList *list.List
}

type hostTimestamp struct {
	host string
	ts   time.Time
}

func New(h Handler, buf int) *Fetcher {
	if buf < 0 {
		buf = DefaultChanBufferSize
	}
	return &Fetcher{
		Handler:        h,
		CrawlDelay:     DefaultCrawlDelay,
		HttpClient:     http.DefaultClient,
		UserAgent:      DefaultUserAgent,
		WorkerIdleTTL:  DefaultWorkerIdleTTL,
		ch:             make(chan Command, buf),
		buf:            buf,
		hosts:          make(map[string]chan Command),
		hostToIdleElem: make(map[string]*list.Element),
		idleList:       list.New(),
	}
}

// Enqueue is a convenience method to send a request to the Fetcher's channel.
// It is the same as `ch <- &fetch.Cmd{U: u, M: method}` where `ch` is the Fetcher's channel
// returned by the call to Fetcher.Start.
func (f *Fetcher) Enqueue(u *url.URL, method string) {
	f.ch <- &Cmd{U: u, M: method}
}

func (f *Fetcher) EnqueueString(rawurl, method string) error {
	parsed, err := url.Parse(rawurl)
	if err != nil {
		return err
	}
	f.ch <- &Cmd{U: parsed, M: method}
	return nil
}

func (f *Fetcher) EnqueueHead(rawurl ...string) error {
	return f.enqueueWithMethod(rawurl, "HEAD")
}

func (f *Fetcher) EnqueueGet(rawurl ...string) error {
	return f.enqueueWithMethod(rawurl, "GET")
}

func (f *Fetcher) enqueueWithMethod(rawurl []string, method string) error {
	for _, v := range rawurl {
		err := f.EnqueueString(v, method)
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *Fetcher) Start() chan<- Command {
	f.wg.Add(1)
	go f.processQueue()
	return f.ch
}

func (f *Fetcher) Stop() {
	close(f.ch)
	f.wg.Wait()
}

func (f *Fetcher) processQueue() {
	for v := range f.ch {
		u := v.URL()
		if u.Host == "" {
			// The URL must be rooted with a host.
			f.Handler.Handle(v, nil, nil, ErrEmptyHost)
			continue
		}
		// Check if a channel is already started for this host
		ch, ok := f.hosts[u.Host]
		if !ok {
			// Start a new channel and goroutine for this host. The chan has the same
			// buffer as the enqueue channel of the Fetcher.

			// Must send the robots.txt request.
			rob, err := u.Parse("/robots.txt")
			if err != nil {
				f.Handler.Handle(v, nil, nil, err)
				continue
			}
			// Create the channel and add it to the hosts map
			ch = make(chan Command, f.buf)
			f.hosts[u.Host] = ch
			f.wg.Add(1)
			// Start the working goroutine for this host
			go f.processChan(ch)
			// Enqueue the robots.txt request first.
			ch <- robotCommand{&Cmd{U: rob, M: "GET"}}
		}
		// Send the request to this channel
		// TODO : Right now, this could deadlock if a handler tries to enqueue and the
		// host goro is full (which is highly possible, given the delays). Host goro waits
		// for handler to finish before dequeuing next one, and handler waits for enqueue
		// to free a spot -> deadlock. Should internally buffer using a list?
		ch <- v
		// Adjust the timestamp for this host's idle TTL
		f.setHostTimestamp(u.Host)
		// Garbage collect idle hosts
		// TODO : Do only once in a while, not on every loop?
		f.freeIdleHosts()
	}
	// Close all host channels now that it is impossible to send on those.
	for _, ch := range f.hosts {
		close(ch)
	}
	f.wg.Done()
}

func (f *Fetcher) setHostTimestamp(host string) {
	if e, ok := f.hostToIdleElem[host]; !ok {
		e = f.idleList.PushBack(&hostTimestamp{host, time.Now()})
		f.hostToIdleElem[host] = e
	} else {
		e.Value.(*hostTimestamp).ts = time.Now()
		f.idleList.MoveToBack(e)
	}
}

func (f *Fetcher) freeIdleHosts() {
	for e := f.idleList.Front(); e != nil; {
		hostts := e.Value.(*hostTimestamp)
		if time.Now().Sub(hostts.ts) > f.WorkerIdleTTL {
			ch := f.hosts[hostts.host]
			// Remove and close this host's channel
			delete(f.hosts, hostts.host)
			close(ch)
			// Remove from idle list
			delete(f.hostToIdleElem, hostts.host)
			newe := e.Next()
			f.idleList.Remove(e)
			// Continue with next element, there may be more the free
			e = newe
		} else {
			// The list is ordered by oldest first, so as soon as one host is not passed
			// its TTL, we can safely exit the loop.
			break
		}
	}
}

func (f *Fetcher) processChan(ch <-chan Command) {
	var (
		agent *robotstxt.Group
		wait  <-chan time.Time
		delay = f.CrawlDelay
	)

	for v := range ch {
		// Wait for the prescribed delay
		if wait != nil {
			<-wait
		}
		switch r, ok := v.(robotCommand); {
		case ok:
			// This is the robots.txt request
			agent = f.getRobotAgent(r)
			// Initialize the crawl delay
			if agent != nil && agent.CrawlDelay > 0 {
				delay = agent.CrawlDelay
			}
			wait = time.After(delay)
		case agent == nil || agent.Test(v.URL().Path):
			// Path allowed, process the request
			res, req, err := f.doRequest(v)
			f.visit(v, res, req, err)
			wait = time.After(delay)
		default:
			// Path disallowed by robots.txt
			f.visit(v, nil, nil, ErrDisallowed)
			wait = nil
		}
	}
	f.wg.Done()
}

func (f *Fetcher) getRobotAgent(r robotCommand) *robotstxt.Group {
	res, _, err := f.doRequest(r)
	if err != nil {
		// TODO: Ignore robots.txt request error?
		fmt.Fprintf(os.Stderr, "fetch: error fetching robots.txt: %s\n", err)
		return nil
	}
	defer res.Body.Close()
	robData, err := robotstxt.FromResponse(res)
	if err != nil {
		// TODO : Ignore robots.txt parse error?
		fmt.Fprintf(os.Stderr, "fetch: error parsing robots.txt: %s\n", err)
		return nil
	}
	return robData.FindGroup(f.UserAgent)
}

func (f *Fetcher) visit(cmd Command, res *http.Response, req *http.Request, err error) {
	if res != nil {
		defer res.Body.Close()
	}
	f.Handler.Handle(cmd, res, req, err)
}

func (f *Fetcher) doRequest(r Command) (*http.Response, *http.Request, error) {
	req, err := http.NewRequest(r.Method(), r.URL().String(), nil)
	if err != nil {
		return nil, nil, err
	}
	ua := r.UserAgent()
	if ua == "" {
		ua = f.UserAgent
	}
	req.Header.Set("User-Agent", ua)
	res, err := f.HttpClient.Do(req)
	if err != nil {
		return nil, req, err
	}
	return res, req, nil
}
