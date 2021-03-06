// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bitly/go-simplejson"
	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/controller/push"
	"go.opentelemetry.io/otel/sdk/metric/processor/basic"
	"go.opentelemetry.io/otel/sdk/metric/selector/simple"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("sample-app")
var meter = otel.Meter("test-meter")

func main() {

	initProvider()

	r := mux.NewRouter()
	r.Use(otelmux.Middleware("my-server"))

	// labels represent additional key-value descriptors that can be bound to a
	// metric observer or recorder.
	commonLabels := []label.KeyValue{
		label.String("labelA", "chocolate"),
		label.String("labelB", "raspberry"),
		label.String("labelC", "vanilla"),
	}

	// Recorder metric example
	valuerecorder := metric.Must(meter).
		NewFloat64Counter(
			"an_important_metric",
			metric.WithDescription("Measures the cumulative epicness of the app"),
		).Bind(commonLabels...)
	defer valuerecorder.Unbind()

	r.HandleFunc("/aws-sdk-call", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Content-Type", "application/json")
		ctx := r.Context()

		xrayTraceID := getXrayTraceID(trace.SpanFromContext(ctx))
		json := simplejson.New()
		json.Set("traceId", xrayTraceID)
		payload, _ := json.MarshalJSON()

		w.Write(payload)

	}))

	r.HandleFunc("/outgoing-http-call", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Content-Type", "application/json")

		client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
		ctx := r.Context()

		xrayTraceID, _ := func(ctx context.Context) (string, error) {

			req, _ := http.NewRequestWithContext(ctx, "GET", "https://aws.amazon.com", nil)

			res, err := client.Do(req)
			if err != nil {
				handleErr(err, "HTTP call to aws.amazon.com failed")
			}

			ioutil.ReadAll(res.Body)
			_ = res.Body.Close()

			return getXrayTraceID(trace.SpanFromContext(ctx)), err

		}(ctx)

		ctx, span := tracer.Start(
			context.Background(),
			"CollectorExporter-Example",
			trace.WithAttributes(commonLabels...))
		defer span.End()
		valuerecorder.Add(ctx, 1.0)

		json := simplejson.New()
		json.Set("traceId", xrayTraceID)
		payload, _ := json.MarshalJSON()

		w.Write(payload)

	}))

	http.Handle("/", r)

	// Start server
	address := os.Getenv("LISTEN_ADDRESS")
	if len(address) > 0 {
		http.ListenAndServe(fmt.Sprintf("%s", address), nil)
	} else {
		// Default port 8000
		http.ListenAndServe("localhost:8080", nil)
	}
}

func initProvider() {

	ctx := context.Background()

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	// Create new OTLP Exporter
	exporter, err := otlp.NewExporter(
		ctx,
		otlp.WithInsecure(),
		otlp.WithAddress(endpoint),
	)
	handleErr(err, "failed to create new OTLP exporter")

	cfg := sdktrace.Config{
		DefaultSampler: sdktrace.AlwaysSample(),
	}
	idg := xray.NewIDGenerator()

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithConfig(cfg),
		sdktrace.WithSyncer(exporter),
		sdktrace.WithIDGenerator(idg),
	)

	pusher := push.New(
		basic.New(
			simple.NewWithExactDistribution(),
			exporter,
		),
		exporter,
		push.WithPeriod(2*time.Second),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(xray.Propagator{})
	otel.SetMeterProvider(pusher.MeterProvider())
	pusher.Start()
}

func getXrayTraceID(span trace.Span) string {
	xrayTraceID := span.SpanContext().TraceID.String()
	result := fmt.Sprintf("1-%s-%s", xrayTraceID[0:8], xrayTraceID[8:])
	return result
}

func handleErr(err error, message string) {
	if err != nil {
		log.Fatalf("%s: %v", message, err)
	}
}
