package telemetry_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github-api-gateway/internal/telemetry"
)

func TestParseTraceParent(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		wantValid  bool
		wantTrace  string
		wantParent string
		wantFlags  string
	}{
		{
			name:       "valid W3C traceparent",
			header:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantValid:  true,
			wantTrace:  "4bf92f3577b34da6a3ce929d0e0e4736",
			wantParent: "00f067aa0ba902b7",
			wantFlags:  "01",
		},
		{
			name:      "empty header",
			header:    "",
			wantValid: false,
		},
		{
			name:      "wrong number of segments",
			header:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
			wantValid: false,
		},
		{
			name:      "unsupported version ff",
			header:    "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantValid: false,
		},
		{
			name:      "uppercase hex in trace-id",
			header:    "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
			wantValid: false,
		},
		{
			name:      "all-zero trace-id",
			header:    "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
			wantValid: false,
		},
		{
			name:      "all-zero parent-id",
			header:    "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
			wantValid: false,
		},
		{
			name:      "short trace-id",
			header:    "00-4bf92f3577b34da6-00f067aa0ba902b7-01",
			wantValid: false,
		},
		{
			name:      "short parent-id",
			header:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa-01",
			wantValid: false,
		},
		{
			name:      "invalid flags",
			header:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc, ok := telemetry.ParseTraceParent(tt.header)
			if ok != tt.wantValid {
				t.Fatalf("ParseTraceParent(%q) valid=%v, want %v", tt.header, ok, tt.wantValid)
			}
			if !tt.wantValid {
				return
			}
			if tc.TraceID != tt.wantTrace {
				t.Errorf("TraceID=%q, want %q", tc.TraceID, tt.wantTrace)
			}
			if tc.ParentSpanID != tt.wantParent {
				t.Errorf("ParentSpanID=%q, want %q", tc.ParentSpanID, tt.wantParent)
			}
			if tc.Flags != tt.wantFlags {
				t.Errorf("Flags=%q, want %q", tc.Flags, tt.wantFlags)
			}
			if len(tc.SpanID) != 16 || tc.SpanID == tt.wantParent {
				t.Errorf("expected new 16-hex child SpanID distinct from ParentSpanID, got %q", tc.SpanID)
			}
		})
	}
}

func TestObservabilityMiddleware_PropagatesChildSpanAndCorrelatesLogs(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	reg := telemetry.NewRegistry(time.Now(), telemetry.GaugeSources{})

	var upstreamTraceParent, upstreamTraceState string
	var ctxTrace telemetry.TraceContext
	var hasCtxTrace bool

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamTraceParent = r.Header.Get("traceparent")
		upstreamTraceState = r.Header.Get("tracestate")
		ctxTrace, hasCtxTrace = telemetry.FromContext(r.Context())
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	})

	mw := telemetry.NewObservabilityMiddleware(reg, logger)
	handler := mw(next)

	incomingTP := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest(http.MethodPost, "/repos/org/repo/issues", nil)
	req.Header.Set("traceparent", incomingTP)
	req.Header.Set("tracestate", "congo=t61rcWkgMzE")
	req.Header.Set("Authorization", "Bearer secret-client-key-do-not-log")
	req.Header.Set("X-Gateway-Key", "secret-gateway-key-do-not-log")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", rec.Code)
	}
	if !hasCtxTrace {
		t.Fatal("expected TraceContext in request context")
	}
	if ctxTrace.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("expected TraceID preserved, got %q", ctxTrace.TraceID)
	}
	if ctxTrace.ParentSpanID != "00f067aa0ba902b7" {
		t.Errorf("expected ParentSpanID 00f067aa0ba902b7, got %q", ctxTrace.ParentSpanID)
	}
	if upstreamTraceParent != ctxTrace.TraceParent() {
		t.Errorf("upstream traceparent=%q, want %q", upstreamTraceParent, ctxTrace.TraceParent())
	}
	if upstreamTraceState != "congo=t61rcWkgMzE" {
		t.Errorf("upstream tracestate=%q, want congo=t61rcWkgMzE", upstreamTraceState)
	}
	if respTP := rec.Header().Get("traceparent"); respTP != ctxTrace.TraceParent() {
		t.Errorf("response traceparent=%q, want %q", respTP, ctxTrace.TraceParent())
	}

	logOutput := logBuf.String()
	for _, wantField := range []string{
		`"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`,
		`"parent_span_id":"00f067aa0ba902b7"`,
		`"span_id":"` + ctxTrace.SpanID + `"`,
		`"method":"POST"`,
		`"path":"/repos/org/repo/issues"`,
		`"status":201`,
	} {
		if !strings.Contains(logOutput, wantField) {
			t.Errorf("expected structured log to contain %s, got:\n%s", wantField, logOutput)
		}
	}

	// Security check: secrets must never appear in logs
	if strings.Contains(logOutput, "secret-client-key-do-not-log") || strings.Contains(logOutput, "secret-gateway-key-do-not-log") {
		t.Fatalf("SECURITY VIOLATION: secret key leaked in structured logs:\n%s", logOutput)
	}
}

func TestObservabilityMiddleware_GeneratesRootTraceWhenMissingOrMalformed(t *testing.T) {
	reg := telemetry.NewRegistry(time.Now(), telemetry.GaugeSources{})
	mw := telemetry.NewObservabilityMiddleware(reg, nil)

	var upstreamTP string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamTP = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/repos/golang/go", nil)
	req.Header.Set("traceparent", "invalid-header")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	respTP := rec.Header().Get("traceparent")
	if respTP == "" || respTP != upstreamTP {
		t.Fatalf("expected matching non-empty generated traceparent on upstream (%q) and response (%q)", upstreamTP, respTP)
	}
	tc, ok := telemetry.ParseTraceParent(respTP)
	if !ok {
		t.Fatalf("generated traceparent %q failed validation", respTP)
	}
	if tc.Flags != "01" {
		t.Errorf("expected default flags 01, got %q", tc.Flags)
	}
}
