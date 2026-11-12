package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"not found", cerrdefs.ErrNotFound, ErrNotFound},
		{"conflict", cerrdefs.ErrConflict, ErrConflict},
		{"invalid", cerrdefs.ErrInvalidArgument, ErrInvalid},
		{"deadline", context.DeadlineExceeded, ErrTimeout},
		{"unavailable", cerrdefs.ErrUnavailable, ErrUnavailable},
		{"dial error", &net.OpError{Op: "dial", Err: errors.New("refused")}, ErrUnavailable},
		{"url error", &url.Error{Op: "Get", URL: "x", Err: errors.New("refused")}, ErrUnavailable},
		{"wrapped not found", fmt.Errorf("wrap: %w", cerrdefs.ErrNotFound), ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify("op", tc.in)
			if !errors.Is(got, tc.want) {
				t.Fatalf("classify(%v) = %v, want %v", tc.in, got, tc.want)
			}
			if !errors.Is(got, tc.in) && !errors.Is(tc.in, context.DeadlineExceeded) {
				t.Errorf("original error lost from chain: %v", got)
			}
		})
	}
}

func TestClassifyPassesThroughUnknownErrors(t *testing.T) {
	boom := errors.New("boom")
	got := classify("op", boom)
	if !errors.Is(got, boom) {
		t.Fatalf("unknown error not preserved: %v", got)
	}
	for _, s := range []error{ErrNotFound, ErrConflict, ErrUnavailable, ErrTimeout, ErrInvalid} {
		if errors.Is(got, s) {
			t.Errorf("unknown error must not match %v", s)
		}
	}
}

func TestClassifyNil(t *testing.T) {
	if classify("op", nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
