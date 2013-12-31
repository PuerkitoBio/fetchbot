package fetch

// TODO : Timeout for hosts without requests for a given time.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/temoto/robotstxt.go"
)

var (
	ErrEmptyHost  = errors.New("fetch: invalid empty host")
	ErrDisallowed = errors.New("fetch: disallowed by robots.txt")
)

type Visitor interface {
	// TODO : This won't work, the whole point to having the Requester interface
	// is so that it is possible to use a struct with custom data, and that information
	// is lost with those parameters.
	Visit(res *http.Response, req *http.Request, err error)
}

type VisitorFunc func(*http.Response, *http.Request, error)

func (v VisitorFunc) Visit(res *http.Response, req *http.Request, err error) {
	v(res, req, err)
}

type Requester interface {
	URL() *url.URL
	Method() string
}

type Request struct {
	u *url.URL
	m string
}

func (r *Request) URL() *url.URL {
	return r.u
}

func (r *Request) Method() string {
	return r.m
}

type robotRequest struct {
	*Request
}

const (
	DefaultChanBufferSize = 10
	DefaultRobotUserAgent = "fetch (https://github.com/PuerkitoBio/fetch)"
)

func LogVisitor(w io.Writer) Visitor {
	return VisitorFunc(func(res *http.Response, req *http.Request, err error) {
		fmt.Fprintf(w, "fetch: %s %s\n", req.Method, req.URL)
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

type Fetcher struct {
	// The Visitor to be called for each request. It is guaranteed that all requests
	// successfully enqueued will produce a Visitor call.
	Visitor Visitor
	// The HttpClient to use for the requests. If nil, defaults to the net/http
	// package's default client.
	HttpClient *http.Client
	// Defines the Robot user-agent string to use for robots.txt validation.
	RobotUserAgent string

	// ch is the channel to enqueue requests for this fetcher.
	ch chan Requester
	// buf is the buffer capacity of the channel
	buf int
	// wg waits for the processQueue func to finish.
	wg sync.WaitGroup

	// hosts maps the host names to its dedicated requests channel.
	hosts map[string]chan Requester
}

func New(client *http.Client, buf int) *Fetcher {
	if buf < 0 {
		buf = DefaultChanBufferSize
	}
	return &Fetcher{
		HttpClient: client,
		ch:         make(chan Requester, buf),
		buf:        buf,
		hosts:      make(map[string]chan Requester),
	}
}

// Enqueue is a convenience method to send a request to the Fetcher's channel.
// It is the same as `ch <- &fetch.Request{u, method}` where `ch` is the Fetcher's channel
// returned by the call to Fetcher.Start.
func (f *Fetcher) Enqueue(u *url.URL, method string) {
	f.ch <- &Request{u, method}
}

func (f *Fetcher) Start() chan<- Requester {
	if f.HttpClient == nil {
		f.HttpClient = http.DefaultClient
	}
	if f.RobotUserAgent == "" {
		f.RobotUserAgent = DefaultRobotUserAgent
	}
	if f.Visitor == nil {
		// Default to log visitor
		f.Visitor = LogVisitor(os.Stdout)
	}
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
			f.Visitor.Visit(nil, &http.Request{URL: u, Method: v.Method()}, ErrEmptyHost)
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
				f.Visitor.Visit(nil, &http.Request{URL: u, Method: v.Method()}, err)
				continue
			}
			// Create the channel and add it to the hosts map
			ch = make(chan Requester, f.buf)
			f.hosts[u.Host] = ch
			f.wg.Add(1)
			// Start the working goroutine for this host
			go f.processChan(ch)
			// Enqueue the robots.txt request first.
			ch <- robotRequest{&Request{rob, "GET"}}
		}
		// Send the request to this channel
		ch <- v
	}
	// Close all host channels now that it is impossible to send on those.
	for _, ch := range f.hosts {
		close(ch)
	}
	f.wg.Done()
}

func (f *Fetcher) processChan(ch <-chan Requester) {
	var agent *robotstxt.Group
	for v := range ch {
		if _, ok := v.(robotRequest); ok {
			// This is the robots.txt request
			res, _, err := f.doRequest(v)
			if err != nil {
				// TODO: Ignore robots.txt request error?
				fmt.Fprintf(os.Stderr, "fetch: error fetching robots.txt: %s\n", err)
				continue
			}
			robData, err := robotstxt.FromResponse(res)
			if err != nil {
				// TODO : Ignore robots.txt parse error?
				fmt.Fprintf(os.Stderr, "fetch: error parsing robots.txt: %s\n", err)
				res.Body.Close()
				continue
			}
			res.Body.Close()
			agent = robData.FindGroup(f.RobotUserAgent)
			continue
		}
		if agent == nil || agent.Test(v.URL().Path) {
			// Process the request
			f.visit(f.doRequest(v))
		} else {
			f.visit(nil, &http.Request{URL: v.URL(), Method: v.Method()}, ErrDisallowed)
		}
	}
	f.wg.Done()
}

func (f *Fetcher) visit(res *http.Response, req *http.Request, err error) {
	if res != nil {
		defer res.Body.Close()
	}
	f.Visitor.Visit(res, req, err)
}

func (f *Fetcher) doRequest(r Requester) (*http.Response, *http.Request, error) {
	req, err := http.NewRequest(r.Method(), r.URL().String(), nil)
	if err != nil {
		return nil, &http.Request{URL: r.URL(), Method: r.Method()}, err
	}
	res, err := f.HttpClient.Do(req)
	if err != nil {
		return nil, req, err
	}
	return res, req, nil
}
