/*
Copyright 2020 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
)

// https://docs.datadoghq.com/api/
const (
	datadogDefaultHost = "https://api.datadoghq.com"

	datadogMetricsQueryPath     = "/api/v1/query"
	datadogAPIKeyValidationPath = "/api/v1/validate"

	datadogAPIKeySecretKey = "datadog_api_key"
	datadogAPIKeyHeaderKey = "DD-API-KEY"

	datadogApplicationKeySecretKey = "datadog_application_key"
	datadogApplicationKeyHeaderKey = "DD-APPLICATION-KEY"

	datadogFromDeltaMultiplierOnMetricInterval = 10
)

// DatadogProvider executes datadog queries
type DatadogProvider struct {
	metricsQueryEndpoint     string
	apiKeyValidationEndpoint string

	timeout        time.Duration
	apiKey         string
	applicationKey string
	fromDelta      int64
}

type datadogResponse struct {
	Series []struct {
		Pointlist [][]float64 `json:"pointlist"`
	}
}

// NewDatadogProvider takes a canary spec, a provider spec and the credentials map, and
// returns a Datadog client ready to execute queries against the API
func NewDatadogProvider(metricInterval string,
	provider flaggerv1.MetricTemplateProvider,
	credentials map[string][]byte) (*DatadogProvider, error) {

	address := provider.Address
	if address == "" {
		address = datadogDefaultHost
	}

	dd := DatadogProvider{
		timeout:                  5 * time.Second,
		metricsQueryEndpoint:     address + datadogMetricsQueryPath,
		apiKeyValidationEndpoint: address + datadogAPIKeyValidationPath,
	}

	if b, ok := credentials[datadogAPIKeySecretKey]; ok {
		dd.apiKey = string(b)
	} else {
		return nil, fmt.Errorf("datadog credentials does not contain datadog_api_key")
	}

	if b, ok := credentials[datadogApplicationKeySecretKey]; ok {
		dd.applicationKey = string(b)
	} else {
		return nil, fmt.Errorf("datadog credentials does not contain datadog_application_key")
	}

	md, err := time.ParseDuration(metricInterval)
	if err != nil {
		return nil, fmt.Errorf("error parsing metric interval: %w", err)
	}

	dd.fromDelta = int64(datadogFromDeltaMultiplierOnMetricInterval * md.Seconds())
	return &dd, nil
}

// RunQuery executes the datadog query against DatadogProvider.metricsQueryEndpoint
// and returns the the first result as float64
func (p *DatadogProvider) RunQuery(query string) (float64, error) {

	req, err := http.NewRequest("GET", p.metricsQueryEndpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("error http.NewRequest: %w", err)
	}

	req.Header.Set(datadogAPIKeyHeaderKey, p.apiKey)
	req.Header.Set(datadogApplicationKeyHeaderKey, p.applicationKey)
	now := time.Now().Unix()
	q := req.URL.Query()
	q.Add("query", query)
	q.Add("from", strconv.FormatInt(now-p.fromDelta, 10))
	q.Add("to", strconv.FormatInt(now, 10))
	req.URL.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(req.Context(), p.timeout)
	defer cancel()
	r, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}

	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return 0, fmt.Errorf("error reading body: %w", err)
	}

	if r.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("error response: %s: %w", string(b), err)
	}

	var res datadogResponse
	if err := json.Unmarshal(b, &res); err != nil {
		return 0, fmt.Errorf("error unmarshaling result: %w, '%s'", err, string(b))
	}

	if len(res.Series) < 1 {
		return 0, fmt.Errorf("invalid response: %s: %w", string(b), ErrNoValuesFound)
	}

	// in case of more than one series in the response, pick the first time series from the response
	pl := res.Series[0].Pointlist
	if len(pl) < 1 {
		return 0, fmt.Errorf("invalid response: %s: %w", string(b), ErrNoValuesFound)
	}

	// pick the first (oldest) timestamp/value pair from the time series, at the beginning of the interval
	// must not pick the newest one from the end of the interval, since it almost always contains an incomplete bucket
	vs := pl[0]
	if len(vs) < 1 {
		return 0, fmt.Errorf("invalid response: %s: %w", string(b), ErrNoValuesFound)
	}

	// return the second element of the pair: the value
	return vs[1], nil
}

// IsOnline calls the Datadog's validation endpoint with api keys
// and returns an error if the validation fails
func (p *DatadogProvider) IsOnline() (bool, error) {
	req, err := http.NewRequest("GET", p.apiKeyValidationEndpoint, nil)
	if err != nil {
		return false, fmt.Errorf("error http.NewRequest: %w", err)
	}

	req.Header.Add(datadogAPIKeyHeaderKey, p.apiKey)
	req.Header.Add(datadogApplicationKeyHeaderKey, p.applicationKey)

	ctx, cancel := context.WithTimeout(req.Context(), p.timeout)
	defer cancel()
	r, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("request failed: %w", err)
	}

	defer r.Body.Close()

	b, err := io.ReadAll(r.Body)
	if err != nil {
		return false, fmt.Errorf("error reading body: %w", err)
	}

	if r.StatusCode != http.StatusOK {
		return false, fmt.Errorf("error response: %s", string(b))
	}

	return true, nil
}
