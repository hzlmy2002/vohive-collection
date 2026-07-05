package e911

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	swusim "github.com/iniwex5/vowifi-go/engine/sim"
	"github.com/iniwex5/vowifi-go/runtimehost/carrier"
)

var (
	ErrUnsupportedProvider     = errors.New("unsupported e911 provider")
	ErrChallengeNotImplemented = errors.New("e911 challenge not implemented")
	ErrWebsheetUnavailable     = errors.New("e911 websheet unavailable")
)

type Identity struct {
	IMSI        string
	IMEI        string
	MCC         string
	MNC         string
	SIPUsername string
	DisplayName string
	CachedToken string
}

type HeaderPair struct {
	Key   string
	Value string
}

type HTTPRequest struct {
	Method  string
	URL     string
	Headers []HeaderPair
	Body    []byte
}

type HTTPResponse struct {
	StatusCode int
	Body       []byte
}

type HTTPClient interface {
	Do(*HTTPRequest) (*HTTPResponse, error)
}

type TraceSink interface {
	Request(*HTTPRequest)
	Response(*HTTPRequest, *HTTPResponse)
	Error(*HTTPRequest, error)
}

type Request struct {
	Carrier     carrier.Config
	Identity    Identity
	AKAProvider swusim.AKAProvider
	Client      HTTPClient
	Trace       TraceSink
}

type WebsheetRequest struct {
	URL         string
	UserData    string
	ContentType string
	Title       string
}

func NewDefaultHTTPClient() HTTPClient {
	return defaultHTTPClient{client: &http.Client{Timeout: 30 * time.Second}}
}

func StartEmergencyAddressUpdate(ctx context.Context, req Request) (WebsheetRequest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	provider := strings.TrimSpace(req.Carrier.E911.Provider)
	if provider == "" || !req.Carrier.E911.Enabled {
		return WebsheetRequest{}, ErrUnsupportedProvider
	}
	url := strings.TrimSpace(req.Carrier.E911.EntitlementURL)
	if strings.EqualFold(provider, "T-Mobile_entitlement") || strings.Contains(strings.ToLower(provider), "t-mobile") {
		if url == "" {
			return WebsheetRequest{}, ErrWebsheetUnavailable
		}
		return WebsheetRequest{
			URL:         url,
			ContentType: "text/html",
			Title:       "Emergency address",
		}, nil
	}
	if strings.Contains(strings.ToLower(provider), "att") {
		return WebsheetRequest{}, ErrChallengeNotImplemented
	}
	return WebsheetRequest{}, ErrUnsupportedProvider
}

type defaultHTTPClient struct {
	client *http.Client
}

func (c defaultHTTPClient) Do(req *HTTPRequest) (*HTTPResponse, error) {
	if req == nil {
		return nil, errors.New("nil e911 http request")
	}
	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequest(method, strings.TrimSpace(req.URL), bytes.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	for _, h := range req.Headers {
		key := strings.TrimSpace(h.Key)
		if key == "" {
			continue
		}
		httpReq.Header.Add(key, h.Value)
	}
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{StatusCode: resp.StatusCode, Body: body}, nil
}
