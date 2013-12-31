package fetch

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
)

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

const DefaultChanBufferSize = 10

type Fetcher struct {
	// The HttpClient to use for the requests. If nil, defaults to the net/http
	// package's default client.
	HttpClient *http.Client

	// ch is the channel to enqueue requests for this fetcher.
	ch chan Requester
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
	}
}

func (f *Fetcher) Start() chan<- Requester {
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
		fmt.Println(v.URL())
	}
	f.wg.Done()
}
