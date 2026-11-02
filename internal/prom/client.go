// Package prom is a small client for the Prometheus HTTP API.
// It supports instant queries that return a vector, which is all the
// collector needs. Using the plain HTTP API keeps the binary small and shows
// exactly what goes over the wire.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sample is one value with its labels.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Querier runs instant queries. The collector depends on this interface,
// so tests can use a fake.
type Querier interface {
	Query(ctx context.Context, query string, at time.Time) ([]Sample, error)
}

// Client talks to one Prometheus server.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for a Prometheus base URL such as http://prometheus:9090.
func New(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Prometheus URL %q", baseURL)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}, nil
}

// Response shape of /api/v1/query.
type response struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Query runs an instant query and returns the vector result.
func (c *Client) Query(ctx context.Context, query string, at time.Time) ([]Sample, error) {
	form := url.Values{"query": {query}}
	if !at.IsZero() {
		form.Set("time", strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Prometheus: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read Prometheus response: %w", err)
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("prometheus returned HTTP %d with a non-JSON body", resp.StatusCode)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus error (%s): %s", r.ErrorType, r.Error)
	}
	if r.Data.ResultType != "vector" {
		return nil, fmt.Errorf("expected a vector result, got %q", r.Data.ResultType)
	}
	out := make([]Sample, 0, len(r.Data.Result))
	for _, res := range r.Data.Result {
		s, ok := res.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("unexpected value type %T", res.Value[1])
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("parse value %q: %w", s, err)
		}
		out = append(out, Sample{Labels: res.Metric, Value: v})
	}
	return out, nil
}
