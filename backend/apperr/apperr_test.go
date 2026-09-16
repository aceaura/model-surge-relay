package apperr

import (
	"errors"
	"net/http"
	"testing"
)

func TestRetryability(t *testing.T) {
	cases := map[Code]bool{
		TargetUnavailable: true,
		PolicyTimeout:     true,
		Internal:          true,
		PolicyError:       false,
		Unauthorized:      false,
		NotFound:          false,
		InvalidRequest:    false,
		Conflict:          false,
		Disabled:          false,
	}
	for code, want := range cases {
		if got := New(code, "x").Retryable; got != want {
			t.Errorf("%s retryable = %t, want %t", code, got, want)
		}
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[Code]int{
		Unauthorized:      http.StatusUnauthorized,
		NotFound:          http.StatusNotFound,
		InvalidRequest:    http.StatusBadRequest,
		Conflict:          http.StatusConflict,
		Disabled:          http.StatusForbidden,
		TargetUnavailable: http.StatusServiceUnavailable,
		PolicyTimeout:     http.StatusServiceUnavailable,
		PolicyError:       http.StatusInternalServerError,
		Internal:          http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := Status(code); got != want {
			t.Errorf("%s status = %d, want %d", code, got, want)
		}
	}
}

func TestFromWrapsForeignErrors(t *testing.T) {
	if From(nil) != nil {
		t.Fatal("From(nil) must be nil")
	}
	own := New(NotFound, "gone")
	if From(own) != own {
		t.Error("From must return the same *Error instance")
	}
	wrapped := From(errors.New("db exploded"))
	if wrapped.Code != Internal {
		t.Errorf("foreign error code = %s, want %s", wrapped.Code, Internal)
	}
}

func TestFieldCarriesFieldName(t *testing.T) {
	e := Field(InvalidRequest, "model_id", "required")
	if e.Field != "model_id" || e.Code != InvalidRequest {
		t.Errorf("unexpected error: %+v", e)
	}
}
