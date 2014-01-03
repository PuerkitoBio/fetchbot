package fetch

import "net/http"

type Context struct {
	Cmd     Command
	Request *http.Request
	Chan    Queue
}

type Handler interface {
	Handle(*http.Response, *Context, error)
}

type HandlerFunc func(*http.Response, *Context, error)

func (h HandlerFunc) Handle(res *http.Response, ctx *Context, err error) {
	h(res, ctx, err)
}
