package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	w3cVersion     = "00"
	defaultFlags   = "01"
	zeroTraceID    = "00000000000000000000000000000000"
	zeroSpanID     = "0000000000000000"
	traceParentHdr = "traceparent"
	traceStateHdr  = "tracestate"
)

type traceContextKey struct{}

// TraceContext holds W3C Trace Context fields for OpenTelemetry propagation and structured logging.
type TraceContext struct {
	Version      string
	TraceID      string
	SpanID       string
	ParentSpanID string
	Flags        string
	TraceState   string
}

// TraceParent formats the W3C traceparent header value (`00-<trace-id>-<span-id>-<flags>`).
func (tc TraceContext) TraceParent() string {
	ver := tc.Version
	if ver == "" {
		ver = w3cVersion
	}
	flags := tc.Flags
	if flags == "" {
		flags = defaultFlags
	}
	return fmt.Sprintf("%s-%s-%s-%s", ver, tc.TraceID, tc.SpanID, flags)
}

// ParseTraceParent validates an incoming W3C traceparent header and returns a new child TraceContext
// with a freshly generated 16-hex SpanID and ParentSpanID set to the caller's span-id.
func ParseTraceParent(header string) (TraceContext, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return TraceContext{}, false
	}

	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return TraceContext{}, false
	}

	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]
	if version != w3cVersion {
		return TraceContext{}, false
	}
	if len(traceID) != 32 || !isLowerHex(traceID) || traceID == zeroTraceID {
		return TraceContext{}, false
	}
	if len(parentID) != 16 || !isLowerHex(parentID) || parentID == zeroSpanID {
		return TraceContext{}, false
	}
	if len(flags) != 2 || !isLowerHex(flags) {
		return TraceContext{}, false
	}

	childSpanID := generateHexID(8)
	if childSpanID == parentID {
		childSpanID = generateHexID(8)
	}

	return TraceContext{
		Version:      w3cVersion,
		TraceID:      traceID,
		SpanID:       childSpanID,
		ParentSpanID: parentID,
		Flags:        flags,
	}, true
}

// NewRootTraceContext generates a new root W3C TraceContext with a cryptographically random 16-byte TraceID and 8-byte SpanID.
func NewRootTraceContext() TraceContext {
	return TraceContext{
		Version: w3cVersion,
		TraceID: generateHexID(16),
		SpanID:  generateHexID(8),
		Flags:   defaultFlags,
	}
}

// WithContext attaches a TraceContext to a context.Context.
func WithContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// FromContext retrieves the TraceContext from a context.Context, if present.
func FromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	tc, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return tc, ok
}

// NewObservabilityMiddleware returns an HTTP middleware that:
// 1. Parses or generates W3C Trace Context (`traceparent` and `tracestate`).
// 2. Propagates the child `traceparent` (and valid `tracestate`) to upstream requests and downstream responses.
// 3. Records request count and duration in `Registry`.
// 4. Emits a trace-correlated structured `slog` record without leaking secrets.
func NewObservabilityMiddleware(reg *Registry, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			tc, valid := ParseTraceParent(r.Header.Get(traceParentHdr))
			if valid {
				tc.TraceState = strings.TrimSpace(r.Header.Get(traceStateHdr))
			} else {
				tc = NewRootTraceContext()
				r.Header.Del(traceStateHdr)
			}

			tpValue := tc.TraceParent()
			r.Header.Set(traceParentHdr, tpValue)
			w.Header().Set(traceParentHdr, tpValue)

			ctx := WithContext(r.Context(), tc)
			r = r.WithContext(ctx)

			sw := &statusRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(sw, r)

			// Ensure traceparent remains set on response headers even if downstream overwrote headers
			if w.Header().Get(traceParentHdr) == "" {
				w.Header().Set(traceParentHdr, tpValue)
			}

			duration := time.Since(start)
			if reg != nil {
				reg.ObserveRequest(r.Method, sw.statusCode, duration)
			}

			if logger != nil {
				attrs := []any{
					slog.String("trace_id", tc.TraceID),
					slog.String("span_id", tc.SpanID),
				}
				if tc.ParentSpanID != "" {
					attrs = append(attrs, slog.String("parent_span_id", tc.ParentSpanID))
				}
				attrs = append(attrs,
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", sw.statusCode),
					slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
				)
				logger.InfoContext(ctx, "http_request", attrs...)
			}
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wroteHeader {
		sr.statusCode = code
		sr.wroteHeader = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.statusCode = http.StatusOK
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func generateHexID(numBytes int) string {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		// Fallback to non-zero deterministic timestamp bytes in the unlikely event of entropy failure
		ns := uint64(time.Now().UnixNano())
		for i := 0; i < numBytes; i++ {
			buf[i] = byte((ns >> ((i % 8) * 8)) | 1)
		}
	}
	// Guarantee not all zeros
	allZero := true
	for _, b := range buf {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		buf[len(buf)-1] = 1
	}
	return hex.EncodeToString(buf)
}
