package fetchbot

import "net/http"

type Context struct {
	Cmd     Command
	Request *http.Request
	Chan    Queue
}

type Handler interface {
	Handle(*Context, *http.Response, error)
}

type HandlerFunc func(*Context, *http.Response, error)

func (h HandlerFunc) Handle(ctx *Context, res *http.Response, err error) {
	h(ctx, res, err)
}
