package fetchbot

import (
	"io"
	"net/http"
	"net/url"
)

// The Command interface defines the methods required by the Fetcher to request
// a resource.
type Command interface {
	URL() *url.URL
	Method() string
}

// TODO : Naming, and take into account those additional interfaces.
type BasicAuth interface {
	Credentials() (string, string)
}

type BodyReader interface {
	Body() io.Reader
}

type BodyKeyValuer interface {
	Values() url.Values
}

type Cookier interface {
	Cookies() []*http.Cookie
}

type Headerer interface {
	Header() http.Header
}

// The Cmd struct defines a basic command implementation.
type Cmd struct {
	U *url.URL
	M string
}

// URL returns the resource targeted by this command.
func (c *Cmd) URL() *url.URL {
	return c.U
}

// Method returns the HTTP verb to use to process this command (i.e. "GET", "HEAD", etc.).
func (c *Cmd) Method() string {
	return c.M
}

// robotCommand is a "sentinel type" used to distinguish the automatically enqueued robots.txt
// command from the user-enqueued commands.
type robotCommand struct {
	*Cmd
}
