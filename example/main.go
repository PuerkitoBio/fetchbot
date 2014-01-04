package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/PuerkitoBio/fetchbot"
	"github.com/PuerkitoBio/goquery"
)

var (
	dup = make(map[string]bool)
	mu  sync.Mutex
)

func ErrHandler(h fetchbot.Handler) fetchbot.Handler {
	return fetchbot.HandlerFunc(func(ctx *fetchbot.Context, res *http.Response, err error) {
		if err != nil {
			fmt.Printf("error: %s %s - %s\n", ctx.Cmd.Method(), ctx.Cmd.URL(), err)
			return
		}
		h.Handle(ctx, res, err)
	})
}

func LinksHandler(h fetchbot.Handler, host string) fetchbot.Handler {
	return fetchbot.HandlerFunc(func(ctx *fetchbot.Context, res *http.Response, err error) {
		// Save as fetched once
		mu.Lock()
		dup[ctx.Cmd.URL().String()] = true
		mu.Unlock()

		// Handle if text/html, otherwise continue. Limit fetched pages to the specified host only
		// (linked pages to other hosts will produce a HEAD request and a log entry, but no further
		// crawling).
		if ctx.Cmd.URL().Host == host && strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
			switch ctx.Cmd.Method() {
			case "GET":
				// Process the body to find the links
				doc, err := goquery.NewDocumentFromResponse(res)
				if err != nil {
					fmt.Printf("error: parse goquery %s - %s\n", ctx.Cmd.URL(), err)
				}
				// Enqueue all links as HEAD requests, unless it is a duplicate
				mu.Lock()
				doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
					val, _ := s.Attr("href")
					// Resolve address
					u, err := ctx.Cmd.URL().Parse(val)
					if err != nil {
						fmt.Printf("error: resolve URL %s - %s\n", val, err)
						return
					}
					if !dup[u.String()] {
						if _, err := ctx.Chan.EnqueueHead(u.String()); err != nil {
							fmt.Printf("error: enqueue head %s - %s\n", u, err)
						} else {
							dup[u.String()] = true
						}
					}
				})
				mu.Unlock()
				// Exit, since logging is done on HEAD
				return

			case "HEAD":
				// Enqueue as a GET, we want the body. Don't check for duplicate, since it is one
				// by definition.
				if _, err := ctx.Chan.EnqueueGet(ctx.Cmd.URL().String()); err != nil {
					fmt.Printf("error: enqueue get %s - %s\n", ctx.Cmd.URL(), err)
				}
			}
		}
		// Continue with wrapped handler
		h.Handle(ctx, res, err)
	})
}

func LogHandler(ctx *fetchbot.Context, res *http.Response, err error) {
	fmt.Printf("%s %s [%d]\n", res.Header.Get("Content-Type"), ctx.Cmd.URL(), res.StatusCode)
}

// TODO : Print mem and goro stats once in a while
func main() {
	const home = "http://golang.org"

	// Create the Fetcher
	f := fetchbot.New(ErrHandler(LinksHandler(fetchbot.HandlerFunc(LogHandler), "golang.org")))
	// Start
	q := f.Start()
	// Enqueue the Go home page
	_, err := q.EnqueueHead(home)
	if err != nil {
		fmt.Printf("error: enqueue head %s - %s\n", home, err)
	}
	// Must be manually stopped (Ctrl-C)
	select {}
}
