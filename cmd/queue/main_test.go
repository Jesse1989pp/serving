/*
Copyright 2019 The Knative Authors
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

package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"knative.dev/serving/pkg/activator"
	"knative.dev/serving/pkg/network"
	"knative.dev/serving/pkg/queue"
)

const wantHost = "a-better-host.com"

func TestHandlerReqEvent(t *testing.T) {
	var httpHandler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(activator.RevisionHeaderName) != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if r.Header.Get(activator.RevisionHeaderNamespace) != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if got, want := r.Host, wantHost; got != want {
			t.Errorf("Host header = %q, want: %q", got, want)
		}
		if got, want := r.Header.Get(network.OriginalHostHeader), ""; got != want {
			t.Errorf("%s header was preserved", network.OriginalHostHeader)
		}

		w.WriteHeader(http.StatusOK)
	}

	server := httptest.NewServer(httpHandler)
	serverURL, _ := url.Parse(server.URL)

	defer server.Close()
	proxy := httputil.NewSingleHostReverseProxy(serverURL)

	params := queue.BreakerParams{QueueDepth: 10, MaxConcurrency: 10, InitialCapacity: 10}
	breaker := queue.NewBreaker(params)
	reqChan := make(chan queue.ReqEvent, 10)
	h := handler(reqChan, breaker, proxy, func() bool { return true })

	writer := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)

	// Verify the Original host header processing.
	req.Host = "nimporte.pas"
	req.Header.Set(network.OriginalHostHeader, wantHost)

	req.Header.Set(network.ProxyHeaderName, activator.Name)
	h(writer, req)
	select {
	case e := <-reqChan:
		if e.EventType != queue.ProxiedIn {
			t.Errorf("Want: %v, got: %v\n", queue.ReqIn, e.EventType)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for an event to be intercepted")
	}
}

func TestProbeHandler(t *testing.T) {
	testcases := []struct {
		name          string
		prober        func() bool
		wantCode      int
		wantBody      string
		requestHeader string
	}{{
		name:          "unexpected probe header",
		prober:        func() bool { return true },
		wantCode:      http.StatusBadRequest,
		wantBody:      fmt.Sprintf(badProbeTemplate, "test-probe"),
		requestHeader: "test-probe",
	}, {
		name:          "true probe function",
		prober:        func() bool { return true },
		wantCode:      http.StatusOK,
		wantBody:      queue.Name,
		requestHeader: queue.Name,
	}, {
		name:          "nil probe function",
		prober:        nil,
		wantCode:      http.StatusInternalServerError,
		wantBody:      "no probe",
		requestHeader: queue.Name,
	}, {
		name:          "false probe function",
		prober:        func() bool { return false },
		wantCode:      http.StatusServiceUnavailable,
		wantBody:      "container not ready",
		requestHeader: queue.Name,
	}}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
			req.Header.Set(network.ProbeHeaderName, tc.requestHeader)

			h := handler(nil, nil, nil, tc.prober)
			h(writer, req)

			if got, want := writer.Code, tc.wantCode; got != want {
				t.Errorf("probe status = %v, want: %v", got, want)
			}
			if got, want := strings.TrimSpace(writer.Body.String()), tc.wantBody; got != want {
				// \r\n might be inserted, etc.
				t.Errorf("probe body = %q, want: %q, diff: %s", got, want, cmp.Diff(got, want))
			}
		})
	}
}

func TestCreateVarLogLink(t *testing.T) {
	dir, err := ioutil.TempDir("", "TestCreateVarLogLink")
	if err != nil {
		t.Errorf("Failed to created temporary directory: %v", err)
	}
	defer os.RemoveAll(dir)
	var env = config{
		ServingNamespace:   "default",
		ServingPod:         "service-7f97f9465b-5kkm5",
		UserContainerName:  "user-container",
		VarLogVolumeName:   "knative-var-log",
		InternalVolumePath: dir,
	}
	createVarLogLink(env)

	source := path.Join(dir, "default_service-7f97f9465b-5kkm5_user-container")
	want := "../knative-var-log"
	got, err := os.Readlink(source)
	if err != nil {
		t.Errorf("Failed to read symlink: %v", err)
	}
	if got != want {
		t.Errorf("Incorrect symlink = %q, want %q, diff: %s", got, want, cmp.Diff(got, want))
	}
}

func TestProbeQueueConnectionFailure(t *testing.T) {
	port := 12345 // some random port (that's not listening)

	if err := probeQueueHealthPath(port, 1); err == nil {
		t.Error("Expected error, got nil")
	}
}

func TestProbeQueueNotReady(t *testing.T) {
	queueProbed := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queueProbed = true
		w.WriteHeader(http.StatusBadRequest)
	}))

	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("%s is not a valid URL: %v", ts.URL, err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("Failed to convert port(%s) to int: %v", u.Port(), err)
	}

	err = probeQueueHealthPath(port, 1)

	if diff := cmp.Diff(err.Error(), "probe returned not ready"); diff != "" {
		t.Errorf("Unexpected not ready message: %s", diff)
	}

	if !queueProbed {
		t.Errorf("Expected the queue proxy server to be probed")
	}
}

func TestProbeQueueReady(t *testing.T) {
	queueProbed := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queueProbed = true
		w.WriteHeader(http.StatusOK)
	}))

	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("%s is not a valid URL: %v", ts.URL, err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("Failed to convert port(%s) to int: %v", u.Port(), err)
	}

	if err = probeQueueHealthPath(port, 1); err != nil {
		t.Errorf("probeQueueHealthPath(%d, 1s) = %s", port, err)
	}

	if !queueProbed {
		t.Errorf("Expected the queue proxy server to be probed")
	}
}

func TestProbeQueueTimeout(t *testing.T) {
	queueProbed := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queueProbed = true
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))

	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("%s is not a valid URL: %v", ts.URL, err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("failed to convert port(%s) to int", u.Port())
	}

	timeout := 1
	if err = probeQueueHealthPath(port, timeout); err == nil {
		t.Errorf("Expected probeQueueHealthPath(%d, %v) to return timeout error", port, timeout)
	}

	ts.Close()

	if !queueProbed {
		t.Errorf("Expected the queue proxy server to be probed")
	}
}

func TestProbeQueueDelayedReady(t *testing.T) {
	count := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count < 9 {
			w.WriteHeader(http.StatusBadRequest)
			count++
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("%s is not a valid URL: %v", ts.URL, err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("Failed to convert port(%s) to int: %v", u.Port(), err)
	}

	timeout := 0
	if err := probeQueueHealthPath(port, timeout); err != nil {
		t.Errorf("probeQueueHealthPath(%d) = %s", port, err)
	}
}
