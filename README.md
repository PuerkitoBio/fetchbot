# fetchbot

Package fetchbot provides a simple and flexible web crawler that follows the robots.txt
policies and crawl delays.

It is very much a rewrite of [gocrawl](https://github.com/PuerkitoBio/gocrawl) with a
simpler API, less features built-in, but at the same time more flexibility. As for Go
itself, sometimes less is more!

[![build status](https://secure.travis-ci.org/PuerkitoBio/fetchbot.png)](http://travis-ci.org/PuerkitoBio/fetchbot)

## Installation

To install, simply run in a terminal:

    go get github.com/PuerkitoBio/fetchbot

The package has a single external dependency, [robotstxt](https://github.com/temoto/robotstxt-go). It also integrates code from the [iq package](https://github.com/kylelemons/iq).

The [API documentation is available on godoc.org](http://godoc.org/github.com/PuerkitoBio/fetchbot).

## Changes

* 2014-07-04 : change the type of Fetcher.HttpClient from `*http.Client` to the `Doer` interface. Low chance of breaking existing code, but it's a possibility if someone used the fetcher's client to run other requests (e.g. `f.HttpClient.Get(...)`).

## Usage

The following example (taken from /example/short/main.go) shows how to create and
start a Fetcher, one way to send commands, and how to stop the fetcher once all
commands have been handled.

    package main

    import (
    	"fmt"
    	"net/http"

    	"github.com/PuerkitoBio/fetchbot"
    )

    func main() {
    	f := fetchbot.New(fetchbot.HandlerFunc(handler))
    	queue := f.Start()
    	queue.SendStringHead("http://google.com", "http://golang.org", "http://golang.org/doc")
    	queue.Close()
    }

    func handler(ctx *fetchbot.Context, res *http.Response, err error) {
    	if err != nil {
    		fmt.Printf("error: %s\n", err)
    		return
    	}
    	fmt.Printf("[%d] %s %s\n", res.StatusCode, ctx.Cmd.Method(), ctx.Cmd.URL())
    }

A more complex and complete example can be found in the repository, at /example/full/.

Basically, a Fetcher is an instance of a web crawler, independent of other Fetchers.
It receives Commands via the Queue, executes the requests, and calls a Handler to
process the responses. A Command is an interface that tells the Fetcher which URL to
fetch, and which HTTP method to use (i.e. "GET", "HEAD", ...).

A call to Fetcher.Start() returns the Queue associated with this Fetcher. This is the
thread-safe object that can be used to send commands, or to stop the crawler.

Both the Command and the Handler are interfaces, and may be implemented in various ways.
They are defined like so:

    type Command interface {
    	URL() *url.URL
    	Method() string
    }
    type Handler interface {
    	Handle(*Context, *http.Response, error)
    }

A Context is a struct that holds the Command and the Queue, so that the Handler always
knows which Command initiated this call, and has a handle to the Queue.

A Handler is similar to the net/http Handler, and middleware-style combinations can
be built on top of it. A HandlerFunc type is provided so that simple functions
with the right signature can be used as Handlers (like net/http.HandlerFunc), and there
is also a multiplexer Mux that can be used to dispatch calls to different Handlers
based on some criteria.

The Fetcher recognizes a number of interfaces that the Command may implement, for
more advanced needs. If the Command implements the BasicAuthProvider interface,
a Basic Authentication header will be put in place with the given credentials
to fetch the URL.

Similarly, the CookiesProvider and HeaderProvider interfaces offer the expected
features (setting cookies and header values on the request). The ReaderProvider
and ValuesProvider interfaces are also supported, although they should be mutually
exclusive as they both set the body of the request. If both are supported, the
ReaderProvider interface is used. It sets the body of the request (e.g. for a "POST")
using the given io.Reader instance. The ValuesProvider does the same, but using
the given url.Values instance, and sets the Content-Type of the body to
"application/x-www-form-urlencoded" (unless it is explicitly set by a HeaderProvider).

Since the Command is an interface, it can be a custom struct that holds additional
information, such as an ID for the URL (e.g. from a database), or a depth counter
so that the crawling stops at a certain depth, etc. For basic commands that don't
require additional information, the package provides the Cmd struct that implements
the Command interface. This is the Command implementation used when using the
various Queue.SendString\* methods.

The Fetcher has a number of fields that provide further customization:

- HttpClient : By default, the Fetcher uses the net/http default Client to make requests. A
different client can be set on the Fetcher.HttpClient field.

- CrawlDelay : That value is used only if there is no delay specified
by the robots.txt of a given host.

- UserAgent : Sets the user agent string to use for the requests and to validate
against the robots.txt entries.

- WorkerIdleTTL : Sets the duration that a worker goroutine can wait without receiving
new commands to fetch. If the idle time-to-live is reached, the worker goroutine
is stopped and its resources are released. This can be especially useful for
long-running crawlers.

What fetchbot doesn't do - especially compared to gocrawl - is that it doesn't
keep track of already visited URLs, and it doesn't normalize the URLs. This is outside
the scope of this package - all commands sent on the Queue will be fetched.
Normalization can easily be done (e.g. using [purell](https://github.com/PuerkitoBio/purell)) before sending the Command to the Fetcher.
How to keep track of visited URLs depends on the use-case of the specific crawler,
but for an example, see /example/full/main.go.

## License

The [BSD 3-Clause license](http://opensource.org/licenses/BSD-3-Clause), the same as
the Go language. The iq package source code is under the CDDL-1.0 license (details in
the source file).
