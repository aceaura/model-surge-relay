package collection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
)

type fakeCatalog struct {
	listings []upstreamclient.Listing
	err      error
	calls    int
}

func (f *fakeCatalog) Models(context.Context) ([]upstreamclient.Listing, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.listings, nil
}

func repoWith(catalog Catalog) *Repo {
	return NewRepo(nil, nil, catalog)
}

func TestValidateReferencesAcceptsKnownIDs(t *testing.T) {
	catalog := &fakeCatalog{listings: []upstreamclient.Listing{
		{ID: "kimi-1/k3", Enabled: true},
		{ID: "ark-1/ds", Enabled: false},
	}}
	r := repoWith(catalog)

	// 目录中已禁用的引用仍可入组：启用状态是运行时判断，不是配置期约束。
	if err := r.validateReferences(context.Background(), []string{"kimi-1/k3", "ark-1/ds"}); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateReferencesRejectsUnknownIDs(t *testing.T) {
	catalog := &fakeCatalog{listings: []upstreamclient.Listing{{ID: "kimi-1/k3", Enabled: true}}}
	r := repoWith(catalog)

	err := r.validateReferences(context.Background(), []string{"kimi-1/k3", "ghost/x", "gone/y"})
	e := apperr.From(err)
	if e == nil || e.Code != apperr.InvalidRequest {
		t.Fatalf("err = %v, want invalid_request", e)
	}
	if e.Field != "members" {
		t.Errorf("field = %q, want members", e.Field)
	}
	for _, want := range []string{"ghost/x", "gone/y"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q must name the unknown reference %q", e.Message, want)
		}
	}
	if strings.Contains(e.Message, "kimi-1/k3") {
		t.Error("message must not blame the valid reference")
	}
}

func TestValidateReferencesSkipsCatalogCallForEmptyList(t *testing.T) {
	catalog := &fakeCatalog{}
	r := repoWith(catalog)

	if err := r.validateReferences(context.Background(), nil); err != nil {
		t.Fatalf("clearing members must be allowed: %v", err)
	}
	if catalog.calls != 0 {
		t.Errorf("catalog calls = %d, want 0 for an empty member list", catalog.calls)
	}
}

func TestValidateReferencesFailsClosedWhenCatalogIsDown(t *testing.T) {
	catalog := &fakeCatalog{err: errors.New("upstream unreachable")}
	r := repoWith(catalog)

	if err := r.validateReferences(context.Background(), []string{"kimi-1/k3"}); err == nil {
		t.Fatal("an unreachable catalog must not let unverified references through")
	}
}
