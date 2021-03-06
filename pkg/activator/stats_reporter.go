/*
Copyright 2018 The Knative Authors

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

package activator

import (
	"context"
	"strconv"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	pkgmetrics "knative.dev/pkg/metrics"
	"knative.dev/pkg/metrics/metricskey"
	"knative.dev/serving/pkg/metrics"
)

var (
	requestConcurrencyM = stats.Int64(
		"request_concurrency",
		"Concurrent requests that are routed to Activator",
		stats.UnitDimensionless)
	requestCountM = stats.Int64(
		"request_count",
		"The number of requests that are routed to Activator",
		stats.UnitDimensionless)
	responseTimeInMsecM = stats.Float64(
		"request_latencies",
		"The response time in millisecond",
		stats.UnitMilliseconds)

	// NOTE: 0 should not be used as boundary. See
	// https://github.com/census-ecosystem/opencensus-go-exporter-stackdriver/issues/98
	defaultLatencyDistribution = view.Distribution(5, 10, 20, 40, 60, 80, 100, 150, 200, 250, 300, 350, 400, 450, 500, 600, 700, 800, 900, 1000, 2000, 5000, 10000, 20000, 50000, 100000)
)

// StatsReporter defines the interface for sending activator metrics
type StatsReporter interface {
	ReportRequestConcurrency(ns, service, config, rev string, v int64) error
	ReportRequestCount(ns, service, config, rev string, responseCode, numTries int) error
	ReportResponseTime(ns, service, config, rev string, responseCode int, d time.Duration) error
}

// reporter holds cached metric objects to report autoscaler metrics
type reporter struct {
	ctx context.Context
}

// NewStatsReporter creates a reporter that collects and reports activator metrics
func NewStatsReporter(pod string) (StatsReporter, error) {
	ctx, err := tag.New(
		context.Background(),
		tag.Upsert(metrics.PodTagKey, pod),
		tag.Upsert(metrics.ContainerTagKey, Name),
	)
	if err != nil {
		return nil, err
	}

	// Create view to see our measurements.
	if err := view.Register(
		&view.View{
			Description: "Concurrent requests that are routed to Activator",
			Measure:     requestConcurrencyM,
			Aggregation: view.LastValue(),
			TagKeys:     append(metrics.CommonRevisionKeys, metrics.PodTagKey, metrics.ContainerTagKey),
		},
		&view.View{
			Description: "The number of requests that are routed to Activator",
			Measure:     requestCountM,
			Aggregation: view.Count(),
			TagKeys: append(metrics.CommonRevisionKeys, metrics.PodTagKey, metrics.ContainerTagKey,
				metrics.ResponseCodeKey, metrics.ResponseCodeClassKey, metrics.NumTriesKey),
		},
		&view.View{
			Description: "The response time in millisecond",
			Measure:     responseTimeInMsecM,
			Aggregation: defaultLatencyDistribution,
			TagKeys: append(metrics.CommonRevisionKeys, metrics.PodTagKey, metrics.ContainerTagKey,
				metrics.ResponseCodeKey, metrics.ResponseCodeClassKey),
		},
	); err != nil {
		return nil, err
	}

	return &reporter{ctx: ctx}, nil
}

func valueOrUnknown(v string) string {
	if v != "" {
		return v
	}
	return metricskey.ValueUnknown
}

// ReportRequestConcurrency captures request concurrency metric with value v.
func (r *reporter) ReportRequestConcurrency(ns, service, config, rev string, v int64) error {
	// Note that service names can be an empty string, so it needs a special treatment.
	ctx, err := tag.New(
		r.ctx,
		tag.Upsert(metrics.NamespaceTagKey, ns),
		tag.Upsert(metrics.ServiceTagKey, valueOrUnknown(service)),
		tag.Upsert(metrics.ConfigTagKey, config),
		tag.Upsert(metrics.RevisionTagKey, rev))
	if err != nil {
		return err
	}

	pkgmetrics.Record(ctx, requestConcurrencyM.M(v))
	return nil
}

// ReportRequestCount captures request count.
func (r *reporter) ReportRequestCount(ns, service, config, rev string, responseCode, numTries int) error {
	// Note that service names can be an empty string, so it needs a special treatment.
	ctx, err := tag.New(
		r.ctx,
		tag.Upsert(metrics.NamespaceTagKey, ns),
		tag.Upsert(metrics.ServiceTagKey, valueOrUnknown(service)),
		tag.Upsert(metrics.ConfigTagKey, config),
		tag.Upsert(metrics.RevisionTagKey, rev),
		tag.Upsert(metrics.ResponseCodeKey, strconv.Itoa(responseCode)),
		tag.Upsert(metrics.ResponseCodeClassKey, responseCodeClass(responseCode)),
		tag.Upsert(metrics.NumTriesKey, strconv.Itoa(numTries)))
	if err != nil {
		return err
	}

	pkgmetrics.Record(ctx, requestCountM.M(1))
	return nil
}

// ReportResponseTime captures response time requests
func (r *reporter) ReportResponseTime(ns, service, config, rev string, responseCode int, d time.Duration) error {
	// Note that service names can be an empty string, so it needs a special treatment.
	ctx, err := tag.New(
		r.ctx,
		tag.Upsert(metrics.NamespaceTagKey, ns),
		tag.Upsert(metrics.ServiceTagKey, valueOrUnknown(service)),
		tag.Upsert(metrics.ConfigTagKey, config),
		tag.Upsert(metrics.RevisionTagKey, rev),
		tag.Upsert(metrics.ResponseCodeKey, strconv.Itoa(responseCode)),
		tag.Upsert(metrics.ResponseCodeClassKey, responseCodeClass(responseCode)))
	if err != nil {
		return err
	}

	pkgmetrics.Record(ctx, responseTimeInMsecM.M(float64(d.Milliseconds())))
	return nil
}

// responseCodeClass converts response code to a string of response code class.
// e.g. The response code class is "5xx" for response code 503.
func responseCodeClass(responseCode int) string {
	// Get the hundred digit of the response code and concatenate "xx".
	return strconv.Itoa(responseCode/100) + "xx"
}
