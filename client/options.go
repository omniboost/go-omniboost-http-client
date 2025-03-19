package client

import (
	"context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"net/http"
	"net/url"
)

func WithHttpClient(httpClient *http.Client) Option {
	return func(client *client) {
		client.baseClient = httpClient

		// if we have oauth2 configured, wrap the given http client with the oauth2 client
		if client.authType == authTypeOAuth2 {
			client.httpClient = getWrappedHttpClient(client.baseClient, client.tokenSource)
		} else {
			client.httpClient = httpClient
		}
	}
}

func WithBasicAuth(username, password string) Option {
	return func(client *client) {
		client.authType = authTypeBasic
		client.userName = username
		client.password = password
	}
}

func WithApiKeyAuth(header, apiKey string) Option {
	return func(client *client) {
		client.authType = authTypeApiKey
		client.keyHeader = header
		client.keyValue = apiKey
	}
}

func getWrappedHttpClient(baseClient *http.Client, source oauth2.TokenSource) *http.Client {
	if baseClient == nil {
		return oauth2.NewClient(context.Background(), source)
	}
	return oauth2.NewClient(
		context.WithValue(context.Background(), oauth2.HTTPClient, baseClient),
		source,
	)
}

func WithOAuth2ClientCredentials(config clientcredentials.Config) Option {
	return func(client *client) {
		client.authType = authTypeOAuth2
		client.tokenSource = config.TokenSource(context.Background())
		client.httpClient = getWrappedHttpClient(client.baseClient, client.tokenSource)
	}
}

func WithOAuth2TokenSource(source oauth2.TokenSource) Option {
	return func(client *client) {
		client.authType = authTypeOAuth2
		client.tokenSource = source
		client.httpClient = getWrappedHttpClient(client.baseClient, client.tokenSource)
	}
}

func WithDebug(debug bool) Option {
	return func(client *client) {
		client.debug = debug
	}
}

func WithUserAgent(userAgent string) Option {
	return func(client *client) {
		client.userAgent = userAgent
	}
}

func WithBaseURL(baseURL url.URL) Option {
	return func(client *client) {
		client.baseURL = &baseURL
	}
}

func WithDisallowUnknownFields(disallowUnknownFields bool) Option {
	return func(client *client) {
		client.disallowUnknownFields = disallowUnknownFields
		client.jsoniterInstance = nil
	}
}

func WithMediaType(mediaType string) Option {
	return func(client *client) {
		client.mediaType = mediaType
	}
}

func WithCharset(charset string) Option {
	return func(client *client) {
		client.charset = charset
	}
}

func WithMaxRetries(maxRetries int) Option {
	return func(client *client) {
		client.maxRetries = maxRetries
	}
}
