package fetchbot

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
	DefaultCrawlDelay    = 5 * time.Second
	DefaultUserAgent     = "Fetchbot (https://github.com/PuerkitoBio/fetchbot)"
	DefaultWorkerIdleTTL = 30 * time.Second
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

func New(h Handler) *Fetcher {
	return &Fetcher{
		Handler:        h,
		CrawlDelay:     DefaultCrawlDelay,
		HttpClient:     http.DefaultClient,
		UserAgent:      DefaultUserAgent,
		WorkerIdleTTL:  DefaultWorkerIdleTTL,
		hosts:          make(map[string]chan Command),
		hostToIdleElem: make(map[string]*list.Element),
		idleList:       list.New(),
	}
}

type Queue chan<- Command

// Enqueue is a convenience method to send a request to the Fetcher's channel.
func (q Queue) Enqueue(rawurl, method string) error {
	_, err := q.enqueueWithMethod([]string{rawurl}, method)
	return err
}

func (q Queue) EnqueueHead(rawurl ...string) (int, error) {
	return q.enqueueWithMethod(rawurl, "HEAD")
}

func (q Queue) EnqueueGet(rawurl ...string) (int, error) {
	return q.enqueueWithMethod(rawurl, "GET")
}

func (q Queue) enqueueWithMethod(rawurl []string, method string) (int, error) {
	for i, v := range rawurl {
		parsed, err := url.Parse(v)
		if err != nil {
			return i, err
		}
		q <- &Cmd{U: parsed, M: method}
	}
	return len(rawurl), nil
}

func (f *Fetcher) Start() Queue {
	f.ch = make(chan Command, 1)
	f.wg.Add(1)
	go f.processQueue()
	return f.ch
}

func (f *Fetcher) Stop() {
	// Close the Queue channel.
	close(f.ch)
	// Wait for goroutines to drain and terminate.
	f.wg.Wait()
	// Reset internal maps and lists so that the fetcher is ready to reuse
	f.hosts = make(map[string]chan Command)
	f.hostToIdleElem = make(map[string]*list.Element)
	f.idleList = list.New()
}

func (f *Fetcher) processQueue() {
	for v := range f.ch {
		u := v.URL()
		if u.Host == "" {
			// The URL must be rooted with a host. Handle on a separate goroutine, the Queue
			// goroutine must not block.
			go f.Handler.Handle(&Context{Cmd: v, Chan: f.ch}, nil, ErrEmptyHost)
			continue
		}
		// Check if a channel is already started for this host
		in, ok := f.hosts[u.Host]
		if !ok {
			// Start a new channel and goroutine for this host. The chan has the same
			// buffer as the enqueue channel of the Fetcher.

			// Must send the robots.txt request.
			rob, err := u.Parse("/robots.txt")
			if err != nil {
				// Handle on a separate goroutine, the Queue goroutine must not block.
				go f.Handler.Handle(&Context{Cmd: v, Chan: f.ch}, nil, err)
				continue
			}
			// Create the infinite queue: the in channel to send on, and the out channel
			// to read from in the host's goroutine, and add to the hosts map
			var out chan Command
			in, out = make(chan Command, 1), make(chan Command, 1)
			f.hosts[u.Host] = in
			f.wg.Add(1)
			// Start the infinite queue goroutine for this host
			go SliceIQ(in, out)
			// Start the working goroutine for this host
			go f.processChan(out)
			// Enqueue the robots.txt request first.
			in <- robotCommand{&Cmd{U: rob, M: "GET"}}
		}
		// Send the request
		in <- v
		// Adjust the timestamp for this host's idle TTL
		f.setHostTimestamp(u.Host)
		// Garbage collect idle hosts
		f.freeIdleHosts()
	}
	// Close all host channels now that it is impossible to send on those. Those are the `in`
	// channels of the infinite queue. It will the drain any pending events, triggering the
	// handlers for each in the worker goro, and then the infinite queue goro will terminate
	// and close the `out` channel, which in turn will terminate the worker goro.
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
			f.visit(v, req, res, err)
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

func (f *Fetcher) visit(cmd Command, req *http.Request, res *http.Response, err error) {
	if res != nil {
		defer res.Body.Close()
	}
	f.Handler.Handle(&Context{Cmd: cmd, Request: req, Chan: f.ch}, res, err)
}

func (f *Fetcher) doRequest(r Command) (*http.Response, *http.Request, error) {
	req, err := http.NewRequest(r.Method(), r.URL().String(), nil)
	if err != nil {
		return nil, nil, err
	}
	// TODO : Take other interfaces into account...
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	res, err := f.HttpClient.Do(req)
	if err != nil {
		return nil, req, err
	}
	return res, req, nil
}
