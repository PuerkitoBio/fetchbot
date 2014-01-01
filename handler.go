package fetch

import "net/http"

type Handler interface {
	Handle(cmd Command, res *http.Response, req *http.Request, err error)
}

type HandlerFunc func(Command, *http.Response, *http.Request, error)

func (h HandlerFunc) Handle(cmd Command, res *http.Response, req *http.Request, err error) {
	h(cmd, res, req, err)
}
