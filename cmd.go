// Copyright 2014 Martin Angers and Contributors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fetchbot

import (
	"io"
	"net/http"
	"net/url"
)

// Command interface defines the methods required by the Fetcher to request
// a resource.
type Command interface {
	URL() *url.URL
	Method() string
}

// BasicAuthProvider interface gets the credentials to use to perform the request
// with Basic Authentication.
type BasicAuthProvider interface {
	BasicAuth() (user string, pwd string)
}

// ReaderProvider interface gets the Reader to use as the Body of the request. It has
// higher priority than the ValuesProvider interface, so that if both interfaces are implemented,
// the ReaderProvider is used.
type ReaderProvider interface {
	Reader() io.Reader
}

// ValuesProvider interface gets the values to send as the Body of the request. It has
// lower priority than the ReaderProvider interface, so that if both interfaces are implemented,
// the ReaderProvider is used. If the request has no explicit Content-Type set, it will be automatically
// set to "application/x-www-form-urlencoded".
type ValuesProvider interface {
	Values() url.Values
}

// CookiesProvider interface gets the cookies to send with the request.
type CookiesProvider interface {
	Cookies() []*http.Cookie
}

// HeaderProvider interface gets the headers to set on the request. If an Authorization
// header is set, it will be overridden by the BasicAuthProvider, if implemented.
type HeaderProvider interface {
	Header() http.Header
}

// Cmd defines a basic Command implementation.
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

// HandlerCmd is a basic Command with its own Handler function that is called
// to handle the HTTP response.
type HandlerCmd struct {
	*Cmd
	HandlerFunc
}

// NewHandlerCmd creates a HandlerCmd for the provided request and callback
// handler function.
func NewHandlerCmd(method, rawURL string, fn func(*Context, *http.Response, error)) (*HandlerCmd, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return &HandlerCmd{&Cmd{parsedURL, method}, HandlerFunc(fn)}, nil
}

// robotCommand is a "sentinel type" used to distinguish the automatically enqueued robots.txt
// command from the user-enqueued commands.
type robotCommand struct {
	*Cmd
}
