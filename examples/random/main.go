// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.

// Example application exposing synthetic RPC latency metrics using
// multiple statistical distributions and Prometheus instrumentation.
package main

import (
	"flag"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type metrics struct {
	rpcDurations          *prometheus.SummaryVec
	rpcDurationsHistogram prometheus.Histogram
}

func NewMetrics(reg prometheus.Registerer, normMean, normDomain float64) *metrics {
	m := &metrics{
		// SummaryVec computes client-side quantiles per service label.
		rpcDurations: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Name: "rpc_durations_seconds",
				Help: "RPC latency distribution in seconds.",
				// Quantile objectives for approximate percentile calculation.
				Objectives: map[float64]float64{
					0.5:  0.05,
					0.9:  0.01,
					0.99: 0.001,
				},
			},
			[]string{"service"},
		),

		// Histogram records observations into fixed buckets for aggregation.
		// Linear buckets are centered around the configured normal distribution.
		// Native histograms use exponential growth; factor 1.1 controls resolution vs. cardinality.
		rpcDurationsHistogram: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "rpc_durations_histogram_seconds",
			Help: "RPC latency distribution in seconds.",
			Buckets: prometheus.LinearBuckets(
				normMean-5*normDomain,
				0.5*normDomain,
				20,
			),
			NativeHistogramBucketFactor: 1.1,
		}),
	}

	reg.MustRegister(m.rpcDurations)
	reg.MustRegister(m.rpcDurationsHistogram)

	return m
}

func main() {
	var (
		addr              = flag.String("listen-address", ":8080", "HTTP listen address.")
		uniformDomain     = flag.Float64("uniform.domain", 0.0002, "Uniform distribution range.")
		normDomain        = flag.Float64("normal.domain", 0.0002, "Normal distribution scale.")
		normMean          = flag.Float64("normal.mean", 0.00001, "Normal distribution mean.")
		oscillationPeriod = flag.Duration("oscillation-period", 10*time.Minute, "Rate oscillation period.")
	)

	flag.Parse()

	// Separate registry avoids global state pollution.
	reg := prometheus.NewRegistry()

	m := NewMetrics(reg, *normMean, *normDomain)

	// Exposes Go build and runtime metadata metrics.
	reg.MustRegister(collectors.NewBuildInfoCollector())

	start := time.Now()

	// Oscillation simulates periodic variation in request rate.
	oscillationFactor := func() float64 {
		return 2 + math.Sin(
			math.Sin(2*math.Pi*float64(time.Since(start))/float64(*oscillationPeriod)),
		)
	}

	// Uniform distribution workload generator.
	go func() {
		for {
			v := rand.Float64() * *uniformDomain
			m.rpcDurations.WithLabelValues("uniform").Observe(v)
			time.Sleep(time.Duration(100*oscillationFactor()) * time.Millisecond)
		}
	}()

	// Normal distribution workload generator with exemplar support.
	go func() {
		for {
			v := (rand.NormFloat64() * *normDomain) + *normMean

			m.rpcDurations.WithLabelValues("normal").Observe(v)

			// Exemplars attach a trace-like identifier to individual observations.
			m.rpcDurationsHistogram.(prometheus.ExemplarObserver).ObserveWithExemplar(
				v,
				prometheus.Labels{
					"dummyID": strconv.Itoa(rand.Intn(100000)),
				},
			)

			time.Sleep(time.Duration(75*oscillationFactor()) * time.Millisecond)
		}
	}()

	// Exponential distribution workload generator (long-tail latency behavior).
	go func() {
		for {
			v := rand.ExpFloat64() / 1e6
			m.rpcDurations.WithLabelValues("exponential").Observe(v)
			time.Sleep(time.Duration(50*oscillationFactor()) * time.Millisecond)
		}
	}()

	// HTTP endpoint exposing metrics for Prometheus scraping.
	http.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
			Registry:          reg,
		},
	))

	log.Fatal(http.ListenAndServe(*addr, nil))
}
