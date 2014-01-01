package fetch

import "net/url"

// The Command interface defines the methods required by the Fetcher to request
// a resource.
type Command interface {
	URL() *url.URL
	Method() string
	UserAgent() string
}

// The Cmd struct defines a basic command implementation.
type Cmd struct {
	U  *url.URL
	M  string
	UA string
}

// URL returns the resource targeted by this command.
func (c *Cmd) URL() *url.URL {
	return c.U
}

// Method returns the HTTP verb to use to process this command (i.e. "GET", "HEAD", etc.).
func (c *Cmd) Method() string {
	return c.M
}

// UserAgent returns the user-agent string to use for the request. If it is empty,
// the UserAgent field of the Fetcher is used.
func (c *Cmd) UserAgent() string {
	return c.UA
}

// robotCommand is a "sentinel type" used to distinguish the automatically enqueued robots.txt
// command from the user-enqueued commands.
type robotCommand struct {
	*Cmd
}
