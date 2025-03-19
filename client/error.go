package client

import (
	"net/http"
	"strings"
)

type (
	ErrorResponse struct {
		message  string
		response *http.Response
		Parent   error
	}
)

var _ error = ErrorResponse{}

func (e ErrorResponse) Unwrap() error {
	return e.Parent
}

func (e ErrorResponse) Error() string {
	if e.Parent == nil {
		return e.message
	}
	return strings.Join([]string{e.message, e.Parent.Error()}, ": ")
}

func (e ErrorResponse) Response() *http.Response {
	return e.response
}

func NewErrorResponse(message string, response *http.Response, parent error) ErrorResponse {
	return ErrorResponse{
		message:  message,
		response: response,
		Parent:   parent,
	}
}
