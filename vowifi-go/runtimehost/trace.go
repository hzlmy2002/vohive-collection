package runtimehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type traceIDKey struct{}

var logger any

func NewTraceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "vowifi"
	}
	return hex.EncodeToString(b[:])
}

func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(traceIDKey{}).(string)
	return strings.TrimSpace(v)
}

func SetLogger(l any) {
	logger = l
}
