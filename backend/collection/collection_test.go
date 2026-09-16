package collection

import "testing"

func sample() Snapshot {
	return Snapshot{
		Name: "prod",
		Groups: []GroupSnapshot{
			{
				Name: "primary", Type: "main", Position: 0,
				Members: []Member{
					{ModelID: "kimi-1/k3", Protocol: "anthropic", Enabled: true, Known: true, Position: 0},
					{ModelID: "ark-1/ds", Protocol: "chat_completions", Enabled: false, Known: true, Position: 1},
				},
			},
			{
				Name: "backup", Type: "fallback", Position: 1,
				Members: []Member{
					{ModelID: "deepseek-1/v4", Protocol: "chat_completions", Enabled: true, Known: true, Position: 0},
				},
			},
			{Name: "empty", Type: "spare", Position: 2, Members: []Member{}},
		},
	}
}

func TestHasRecognisesOnlyOwnMembers(t *testing.T) {
	s := sample()
	for _, id := range []string{"kimi-1/k3", "ark-1/ds", "deepseek-1/v4"} {
		if !s.Has(id) {
			t.Errorf("Has(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"kimi-2/k3", "", "kimi-1"} {
		if s.Has(id) {
			t.Errorf("Has(%q) = true, want false", id)
		}
	}
}

func TestLocateReturnsOwningGroup(t *testing.T) {
	s := sample()
	g, ok := s.Locate("deepseek-1/v4")
	if !ok {
		t.Fatal("Locate must find an existing member")
	}
	if g.Name != "backup" || g.Type != "fallback" {
		t.Errorf("group = %s/%s, want backup/fallback", g.Name, g.Type)
	}
	if _, ok := s.Locate("ghost/x"); ok {
		t.Error("Locate must not find a foreign reference")
	}
}

func TestMemberCarriesCatalogAttributes(t *testing.T) {
	s := sample()
	m, ok := s.Member("ark-1/ds")
	if !ok {
		t.Fatal("Member must find an existing member")
	}
	if m.Protocol != "chat_completions" || m.Enabled {
		t.Errorf("member = %+v, want disabled chat_completions", m)
	}
}

func TestEmptyGroupIsAllowed(t *testing.T) {
	s := sample()
	for _, g := range s.Groups {
		if g.Name == "empty" {
			if len(g.Members) != 0 {
				t.Errorf("empty group has %d members", len(g.Members))
			}
			return
		}
	}
	t.Fatal("empty group must survive in the snapshot")
}

func TestSameReferenceMayAppearInMultipleGroups(t *testing.T) {
	s := Snapshot{
		Name: "prod",
		Groups: []GroupSnapshot{
			{Name: "a", Type: "main", Members: []Member{{ModelID: "kimi-1/k3", Known: true}}},
			{Name: "b", Type: "spare", Members: []Member{{ModelID: "kimi-1/k3", Known: true}}},
		},
	}
	if !s.Has("kimi-1/k3") {
		t.Fatal("shared reference must be visible")
	}
	// Locate 返回首个命中组，顺序由组 position 决定。
	g, _ := s.Locate("kimi-1/k3")
	if g.Name != "a" {
		t.Errorf("Locate returned %q, want the first group in order", g.Name)
	}
}

func TestUnknownReferenceKeepsPlaceholderMember(t *testing.T) {
	s := Snapshot{
		Name: "prod",
		Groups: []GroupSnapshot{
			{Name: "a", Type: "main", Members: []Member{{ModelID: "gone/model", Known: false}}},
		},
	}
	m, ok := s.Member("gone/model")
	if !ok {
		t.Fatal("a vanished catalog entry must still appear as a member")
	}
	if m.Known || m.Enabled {
		t.Errorf("member = %+v, want unknown and disabled", m)
	}
}
