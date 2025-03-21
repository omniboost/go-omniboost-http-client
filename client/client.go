package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	jsoniter "github.com/json-iterator/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"text/template"
	"time"
)

const (
	mediaType      = "application/json"
	libraryVersion = "0.0.1"
	userAgent      = "omniboost/" + libraryVersion
	defaultCharset = "utf-8"
)

const (
	authTypeNone = iota
	authTypeBasic
	authTypeApiKey
	authTypeOAuth2
	authTypePreflight
)

type (
	client struct {
		httpClient            *http.Client
		baseClient            *http.Client
		parentClient          Client
		debug                 bool
		userAgent             string
		mediaType             string
		charset               string
		baseURL               *url.URL
		disallowUnknownFields bool
		useCookies            bool

		authType          int
		userName          string
		password          string
		keyHeader         string
		keyValue          string
		maxRetries        int
		tokenSource       oauth2.TokenSource
		jsoniterInstance  jsoniter.API
		preflightAuthFunc func(req *http.Request, client Client) (*http.Request, error)
	}

	Client interface {
		ApplyOption(options Option)
		Do(ctx context.Context, request Request, response interface{}) error
		GetJsoniter() jsoniter.API
		GetParentClient() Client

		private() // just here to make sure only our package can implement this interface
	}

	Option func(*client)

	Request interface {
		Method() string
		PathTemplate() string
	}

	RequestWithParsableErrors interface {
		Request
		ErrorStructs() []error
	}

	RequestWithBody interface {
		Request
		Body() any
	}

	RequestWithAuthPreference interface {
		Request
		SkipAuth() bool
	}

	ContextKey string
)

const (
	contextKeyAttempt = ContextKey("attempt")
)

func (c *client) ApplyOption(options Option) {
	options(c)
}

func (c *client) private() {
}

var _ Client = (*client)(nil)

func NewClient(opts ...Option) Client {
	c := &client{
		userAgent:  userAgent,
		mediaType:  mediaType,
		httpClient: http.DefaultClient,
		charset:    defaultCharset,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *client) GetParentClient() Client {
	return c.parentClient
}

func (c *client) Do(ctx context.Context, request Request, response interface{}) error {
	if c.baseURL == nil {
		return errors.New("client base URL not set")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		_, span = span.TracerProvider().Tracer("kahn").Start(
			ctx,
			"http request",
		)
		defer span.End()
	}

	if c.useCookies {
		if c.httpClient.Jar == nil {
			c.httpClient.Jar, _ = cookiejar.New(nil)
		}
	} else {
		c.httpClient.Jar = nil
	}

	// todo: add ratelimiting etc

	req, err := getHttpRequest(ctx, request, *c.baseURL)
	if err != nil {
		span.RecordError(err, trace.WithStackTrace(true))
		return err
	}
	span.SetAttributes(
		attribute.String("http.method", req.Method),
		attribute.String("http.url", req.URL.String()),
	)

	skipAuth := false
	if reqWithAuthPreference, ok := request.(RequestWithAuthPreference); ok {
		skipAuth = reqWithAuthPreference.SkipAuth()
	}

	if !skipAuth {
		switch c.authType {
		case authTypeBasic:
			req.SetBasicAuth(c.userName, c.password)
		case authTypeApiKey:
			req.Header.Add(c.keyHeader, c.keyValue)
		case authTypePreflight:
			if c.preflightAuthFunc != nil {
				req, err = c.preflightAuthFunc(req, c)
				if err != nil {
					span.RecordError(err, trace.WithStackTrace(true))
					return err
				}
			}
		default:
		}
	}

	// set other headers
	req.Header.Add("Content-Type", fmt.Sprintf("%s; charset=%s", c.mediaType, c.charset))
	req.Header.Add("Accept", c.mediaType)
	req.Header.Add("User-Agent", c.userAgent)

	if c.debug {
		dump, _ := httputil.DumpRequestOut(req, true)
		log.Println(string(dump))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		span.RecordError(err, trace.WithStackTrace(true))

		if c.debug {
			log.Printf("Request failed: %s", err.Error())
		}
		attempt, _ := ctx.Value(contextKeyAttempt).(int)
		if attempt < c.maxRetries {
			time.Sleep(100 * time.Millisecond)
			ctx = context.WithValue(ctx, contextKeyAttempt, attempt+1)
			span.End()
			return c.Do(ctx, request, response)
		}

		return fmt.Errorf("failed to do http request: %w", err)
	}
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	// we always run the dump response so we have a no-op io.Reader to read the body
	dump, _ := httputil.DumpResponse(resp, true)
	if c.debug {
		log.Println(string(dump))
	}

	errorStructs := make([]error, 0)
	if reqWithErrors, ok := request.(RequestWithParsableErrors); ok {
		errorStructs = reqWithErrors.ErrorStructs()
	}

	// todo: untested, since our test api has no response bodies
	if errResponse := checkForErrorResponse(resp); errResponse != nil {
		if err := c.Unmarshal(resp.Body, errorStructs); err != nil {
			return *errResponse
		}

		errs := make([]error, 0)
		for _, e := range errorStructs {
			if e.Error() != "" {
				errs = append(errs, e)
			}
		}
		errResponse.Parent = errors.Join(errs...)

		span.RecordError(err, trace.WithStackTrace(true))
		return *errResponse
	}

	if response == nil {
		return nil
	}

	possibleStructs := []any{response}
	for _, e := range errorStructs {
		possibleStructs = append(possibleStructs, &e)
	}
	if err := c.Unmarshal(resp.Body, possibleStructs...); err != nil {
		span.RecordError(err, trace.WithStackTrace(true))
		return NewErrorResponse("failed to unmarshal response", resp, err)
	}

	// todo: untested, since our test api has no error response bodies
	for _, e := range errorStructs {
		if e.Error() != "" {
			span.RecordError(err, trace.WithStackTrace(true))
			return NewErrorResponse("error in response", resp, e)
		}
	}

	return nil
}

func (c *client) Unmarshal(r io.Reader, vv ...interface{}) error {
	if len(vv) == 0 {
		return nil
	}

	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	var errs []error
	for _, v := range vv {
		err := c.GetJsoniter().Unmarshal(b, &v)
		if err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, err)
		}

	}

	if len(errs) == len(vv) {
		// Everything errored
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return errors.New(strings.Join(msgs, ", "))
	}

	return nil
}

func checkForErrorResponse(r *http.Response) *ErrorResponse {
	if r.StatusCode >= 200 && r.StatusCode <= 299 {
		return nil
	}

	err := NewErrorResponse(r.Status, r, nil)
	return &err
}

func getHttpRequest(ctx context.Context, request Request, baseUrl url.URL) (*http.Request, error) {
	pathParams := getTaggedFields(request, "path")
	queryParams := getTaggedFields(request, "query")

	parsed, err := url.Parse(request.PathTemplate())
	if err != nil {
		return nil, fmt.Errorf("invalid path template: %w", err)
	}

	requestUrl := baseUrl
	q := requestUrl.Query()
	for k, vv := range parsed.Query() {
		for _, v := range vv {
			q.Add(k, v)
		}
	}
	for k, v := range queryParams {
		q.Add(k, fmt.Sprintf("%v", v))
	}
	requestUrl.RawQuery = q.Encode()
	requestUrl.Path = path.Join(requestUrl.Path, parsed.Path)

	if len(pathParams) > 0 {
		tmpl, err := template.New("path").Parse(requestUrl.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to parse path template: %w", err)
		}

		buf := new(bytes.Buffer)
		if err = tmpl.Execute(buf, pathParams); err != nil {
			log.Fatal(err)
		}

		requestUrl.Path = buf.String()
	}

	body, err := getRequestBody(request)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, request.Method(), requestUrl.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create new http request: %w", err)
	}
	return req, nil
}

func getRequestBody(r Request) (io.Reader, error) {
	var body io.Reader

	if rb, ok := r.(RequestWithBody); ok {
		switch b := rb.Body().(type) {
		case io.Reader:
			body = b
		case []byte:
			body = bytes.NewReader(b)
		case string:
			body = bytes.NewReader([]byte(b))
		default:
			buf := new(bytes.Buffer)
			err := jsoniter.NewEncoder(buf).Encode(rb.Body())
			if err != nil {
				return nil, fmt.Errorf("failed to encode request body: %w", err)
			}
			body = buf
		}
	}
	return body, nil
}

type isZeroer interface {
	IsZero() bool
}

func getTaggedFields(elem interface{}, tag string) map[string]interface{} {
	fields := make(map[string]interface{})
	v := reflect.ValueOf(elem)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fields
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := v.Field(i)
		if tagValue, ok := t.Field(i).Tag.Lookup(tag); ok {
			parts := strings.Split(tagValue, ",")
			tagValue = parts[0]

			if field.Kind() == reflect.Pointer && field.IsNil() {
				continue
			} else if field.Kind() == reflect.Pointer {
				field = field.Elem()
			}
			raw := field.Interface()
			if slices.Contains(parts, "omitempty") {
				if field.IsZero() {
					continue
				}
				if zeroer, ok := raw.(isZeroer); ok && zeroer.IsZero() {
					continue
				}
			}
			fields[tagValue] = raw
		}
	}

	return fields
}

func (c *client) GetJsoniter() jsoniter.API {
	if c.jsoniterInstance == nil {
		c.jsoniterInstance = jsoniter.Config{
			EscapeHTML:             true,
			SortMapKeys:            true,
			ValidateJsonRawMessage: true,
			DisallowUnknownFields:  c.disallowUnknownFields,
		}.Froze()
	}
	return c.jsoniterInstance
}
