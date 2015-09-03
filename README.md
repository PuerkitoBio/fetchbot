# fetchbot [![build status](https://secure.travis-ci.org/PuerkitoBio/fetchbot.png)](http://travis-ci.org/PuerkitoBio/fetchbot) [![GoDoc](https://godoc.org/github.com/PuerkitoBio/fetchbot?status.png)](http://godoc.org/github.com/PuerkitoBio/fetchbot)

Package fetchbot provides a simple and flexible web crawler that follows the robots.txt
policies and crawl delays.

It is very much a rewrite of [gocrawl](https://github.com/PuerkitoBio/gocrawl) with a
simpler API, less features built-in, but at the same time more flexibility. As for Go
itself, sometimes less is more!

## Installation

To install, simply run in a terminal:

    go get github.com/PuerkitoBio/fetchbot

The package has a single external dependency, [robotstxt](https://github.com/temoto/robotstxt-go). It also integrates code from the [iq package](https://github.com/kylelemons/iq).

The [API documentation is available on godoc.org](http://godoc.org/github.com/PuerkitoBio/fetchbot).

## Changes

* 2015-07-25 : add `Cancel` method on the `Queue`, to close and drain without requesting any pending commands, unlike `Close` that waits for all pending commands to be processed (thanks to [@buro9][buro9] for the feature request).
* 2015-07-24 : add `HandlerCmd` and call the Command's `Handler` function if it implements the `Handler` interface, bypassing the `Fetcher`'s handler. Support a `Custom` matcher on the `Mux`, using a predicate. (thanks to [@mmcdole][mmcdole] for the feature requests).
* 2015-06-18 : add `Scheme` criteria on the muxer (thanks to [@buro9][buro9]).
* 2015-06-10 : add `DisablePoliteness` field on the `Fetcher` to optionally bypass robots.txt checks (thanks to [@oli-g][oli]).
* 2014-07-04 : change the type of Fetcher.HttpClient from `*http.Client` to the `Doer` interface. Low chance of breaking existing code, but it's a possibility if someone used the fetcher's client to run other requests (e.g. `f.HttpClient.Get(...)`).

## Usage

The following example (taken from /example/short/main.go) shows how to create and
start a Fetcher, one way to send commands, and how to stop the fetcher once all
commands have been handled.

```go
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
```

A more complex and complete example can be found in the repository, at /example/full/.

### Fetcher

Basically, a **Fetcher** is an instance of a web crawler, independent of other Fetchers.
It receives Commands via the **Queue**, executes the requests, and calls a **Handler** to
process the responses. A **Command** is an interface that tells the Fetcher which URL to
fetch, and which HTTP method to use (i.e. "GET", "HEAD", ...).

A call to Fetcher.Start() returns the Queue associated with this Fetcher. This is the
thread-safe object that can be used to send commands, or to stop the crawler.

Both the Command and the Handler are interfaces, and may be implemented in various ways.
They are defined like so:

```go
type Command interface {
	URL() *url.URL
	Method() string
}
type Handler interface {
	Handle(*Context, *http.Response, error)
}
```

A **Context** is a struct that holds the Command and the Queue, so that the Handler always
knows which Command initiated this call, and has a handle to the Queue.

A Handler is similar to the net/http Handler, and middleware-style combinations can
be built on top of it. A HandlerFunc type is provided so that simple functions
with the right signature can be used as Handlers (like net/http.HandlerFunc), and there
is also a multiplexer Mux that can be used to dispatch calls to different Handlers
based on some criteria.

### Command-related Interfaces

The Fetcher recognizes a number of interfaces that the Command may implement, for
more advanced needs.

* `BasicAuthProvider`: Implement this interface to specify the basic authentication
credentials to set on the request.

* `CookiesProvider`: If the Command implements this interface, the provided Cookies
will be set on the request.

* `HeaderProvider`: Implement this interface to specify the headers to set on the
request. 

* `ReaderProvider`: Implement this interface to set the body of the request, via
an `io.Reader`.

* `ValuesProvider`: Implement this interface to set the body of the request, as
form-encoded values. If the Content-Type is not specifically set via a `HeaderProvider`,
it is set to "application/x-www-form-urlencoded". `ReaderProvider` and `ValuesProvider` 
should be mutually exclusive as they both set the body of the request. If both are 
implemented, the `ReaderProvider` interface is used.

* `Handler`: Implement this interface if the Command's response should be handled
by a specific callback function. By default, the response is handled by the Fetcher's
Handler, but if the Command implements this, this handler function takes precedence
and the Fetcher's Handler is ignored.

Since the Command is an interface, it can be a custom struct that holds additional
information, such as an ID for the URL (e.g. from a database), or a depth counter
so that the crawling stops at a certain depth, etc. For basic commands that don't
require additional information, the package provides the Cmd struct that implements
the Command interface. This is the Command implementation used when using the
various Queue.SendString\* methods.

There is also a convenience `HandlerCmd` struct for the commands that should be handled
by a specific callback function. It is a Command with a Handler interface implementation.

### Fetcher Options

The Fetcher has a number of fields that provide further customization:

* HttpClient : By default, the Fetcher uses the net/http default Client to make requests. A
different client can be set on the Fetcher.HttpClient field.

* CrawlDelay : That value is used only if there is no delay specified
by the robots.txt of a given host.

* UserAgent : Sets the user agent string to use for the requests and to validate
against the robots.txt entries.

* WorkerIdleTTL : Sets the duration that a worker goroutine can wait without receiving
new commands to fetch. If the idle time-to-live is reached, the worker goroutine
is stopped and its resources are released. This can be especially useful for
long-running crawlers.

* AutoClose : If true, closes the queue automatically once the number of active hosts
reach 0.

* DisablePoliteness : If true, ignores the robots.txt policies of the hosts.

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

[oli]: https://github.com/oli-g
[buro9]: https://github.com/buro9
[mmcdole]: https://github.com/mmcdole
