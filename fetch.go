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

type Handler interface {
	Handle(cmd Command, res *http.Response, req *http.Request, err error)
}

type HandlerFunc func(Command, *http.Response, *http.Request, error)

func (h HandlerFunc) Handle(cmd Command, res *http.Response, req *http.Request, err error) {
	h(cmd, res, req, err)
}

type Command interface {
	URL() *url.URL
	Method() string
	UserAgent() string
}

type BasicCommand struct {
	u  *url.URL
	m  string
	ua string
}

func (b *BasicCommand) URL() *url.URL {
	return b.u
}

func (b *BasicCommand) Method() string {
	return b.m
}

func (b *BasicCommand) UserAgent() string {
	return b.ua
}

type robotCommand struct {
	*BasicCommand
}

const (
	DefaultChanBufferSize = 10
	DefaultUserAgent      = "fetch (https://github.com/PuerkitoBio/fetch)"
)

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

type Fetcher struct {
	// The Handler to be called for each request. It is guaranteed that all requests
	// successfully enqueued will produce a Handler call.
	Handler Handler
	// The HttpClient to use for the requests. If nil, defaults to the net/http
	// package's default client.
	HttpClient *http.Client
	// Defines the user-agent string to use for robots.txt validation and URL fetching.
	UserAgent string

	// ch is the channel to enqueue requests for this fetcher.
	ch chan Command
	// buf is the buffer capacity of the channel
	buf int
	// wg waits for the processQueue func to finish.
	wg sync.WaitGroup
	// hosts maps the host names to its dedicated requests channel.
	hosts map[string]chan Command
}

func New(h Handler, buf int) *Fetcher {
	if buf < 0 {
		buf = DefaultChanBufferSize
	}
	return &Fetcher{
		Handler:    h,
		HttpClient: http.DefaultClient,
		ch:         make(chan Command, buf),
		buf:        buf,
		hosts:      make(map[string]chan Command),
		UserAgent:  DefaultUserAgent,
	}
}

// Enqueue is a convenience method to send a request to the Fetcher's channel.
// It is the same as `ch <- &fetch.BasicCommand{u, method, ""}` where `ch` is the Fetcher's channel
// returned by the call to Fetcher.Start.
func (f *Fetcher) Enqueue(u *url.URL, method string) {
	f.ch <- &BasicCommand{u: u, m: method}
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
			ch <- robotCommand{&BasicCommand{u: rob, m: "GET"}}
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

func (f *Fetcher) processChan(ch <-chan Command) {
	var agent *robotstxt.Group
	for v := range ch {
		if _, ok := v.(robotCommand); ok {
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
			agent = robData.FindGroup(f.UserAgent)
			continue
		}
		if agent == nil || agent.Test(v.URL().Path) {
			// Process the request
			res, req, err := f.doRequest(v)
			f.visit(v, res, req, err)
		} else {
			f.visit(v, nil, nil, ErrDisallowed)
		}
	}
	f.wg.Done()
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
