package admin

import "testing"

func TestResolveKey_DirectMode(t *testing.T) {
	res, err := ResolveKey(ResolveInput{
		DirectKey:       "ley_abc",
		DirectServer:    "https://srv-a.example",
		PositionalVault: "",
		Keystore:        nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "ley_abc" || res.ServerURL != "https://srv-a.example" {
		t.Fatalf("%+v", res)
	}
}

func TestResolveKey_DirectMode_RequiresServer(t *testing.T) {
	if _, err := ResolveKey(ResolveInput{DirectKey: "ley_x"}); err == nil {
		t.Fatal("expected error for --key without --server")
	}
}

func TestResolveKey_DirectMode_HostMismatch(t *testing.T) {
	_, err := ResolveKey(ResolveInput{
		DirectKey:       "ley_abc",
		DirectServer:    "https://srv-a.example",
		PositionalVault: "srv-b.example/foo",
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestResolveKey_VaultScoped_StepA_OneMatch(t *testing.T) {
	rows := []KeyRow{
		{Vault: "srv-a.example/myvault", Key: "ley_1", Name: "work"},
		{Vault: "srv-a.example/other", Key: "ley_2", Name: "admin"},
	}
	res, err := ResolveKey(ResolveInput{
		PositionalVault: "srv-a.example/myvault",
		Keystore:        rows,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "ley_1" || res.ServerURL != "https://srv-a.example" {
		t.Fatalf("%+v", res)
	}
}

func TestResolveKey_VaultScoped_StepA_MultiNeedsKeyname(t *testing.T) {
	rows := []KeyRow{
		{Vault: "srv-a.example/myvault", Key: "ley_1", Name: "work"},
		{Vault: "srv-a.example/myvault", Key: "ley_2", Name: "recovery"},
	}
	_, err := ResolveKey(ResolveInput{
		PositionalVault: "srv-a.example/myvault",
		Keystore:        rows,
	})
	if err == nil {
		t.Fatal("expected --keyname required error")
	}
	res, err := ResolveKey(ResolveInput{
		PositionalVault: "srv-a.example/myvault",
		Keyname:         "recovery",
		Keystore:        rows,
	})
	if err != nil || res.Key != "ley_2" {
		t.Fatalf("%+v / %v", res, err)
	}
}

func TestResolveKey_VaultScoped_StepBFallback(t *testing.T) {
	rows := []KeyRow{
		{Vault: "srv-a.example/ops", Key: "ley_swa", Name: "operator"},
	}
	res, err := ResolveKey(ResolveInput{
		PositionalVault: "srv-a.example/team-notes",
		Keystore:        rows,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "ley_swa" {
		t.Fatalf("%+v", res)
	}
}

func TestResolveKey_ServerScoped_MultiNeedsKeyname(t *testing.T) {
	rows := []KeyRow{
		{Vault: "srv-a.example/ops", Key: "ley_a", Name: "srv-a-admin"},
		{Vault: "srv-b.example/ops", Key: "ley_b", Name: "srv-b-admin"},
	}
	_, err := ResolveKey(ResolveInput{Keystore: rows})
	if err == nil {
		t.Fatal("expected --keyname required")
	}
	res, err := ResolveKey(ResolveInput{Keyname: "srv-a-admin", Keystore: rows})
	if err != nil || res.Key != "ley_a" {
		t.Fatalf("%+v / %v", res, err)
	}
}

func TestResolveKey_NoCandidates(t *testing.T) {
	_, err := ResolveKey(ResolveInput{
		PositionalVault: "srv-x.example/y",
		Keystore:        []KeyRow{},
	})
	if err == nil {
		t.Fatal("expected no-key error")
	}
}
