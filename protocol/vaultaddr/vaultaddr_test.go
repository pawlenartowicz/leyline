package vaultaddr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpus runs every case in testdata/vault_address/cases.json through
// Parse / Format / Normalize / DialURL / APIURL.
func TestCorpus(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("..", "testdata", "vault_address", "cases.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(blob, &cases); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			host, vaultID, err := Parse(tc.Input)
			if tc.WantError != "" {
				if err == nil {
					t.Fatalf("Parse(%q): want error containing %q, got nil", tc.Input, tc.WantError)
				}
				if !strings.Contains(err.Error(), tc.WantError) {
					t.Fatalf("Parse(%q): want error containing %q, got %q", tc.Input, tc.WantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.Input, err)
			}
			if host != tc.WantHost {
				t.Errorf("Parse(%q) host: got %q, want %q", tc.Input, host, tc.WantHost)
			}
			if vaultID != tc.WantVaultID {
				t.Errorf("Parse(%q) vaultID: got %q, want %q", tc.Input, vaultID, tc.WantVaultID)
			}
			if got := Format(host, vaultID); got != tc.WantNormalized {
				t.Errorf("Format(%q,%q) = %q, want %q", host, vaultID, got, tc.WantNormalized)
			}
			if got, err := Normalize(tc.Input); err != nil || got != tc.WantNormalized {
				t.Errorf("Normalize(%q) = (%q, %v), want (%q, nil)", tc.Input, got, err, tc.WantNormalized)
			}
			if got, err := DialURL(tc.Input); err != nil || got != tc.WantDialURL {
				t.Errorf("DialURL(%q) = (%q, %v), want (%q, nil)", tc.Input, got, err, tc.WantDialURL)
			}
			if got, err := APIURL(tc.Input); err != nil || got != tc.WantAPIURL {
				t.Errorf("APIURL(%q) = (%q, %v), want (%q, nil)", tc.Input, got, err, tc.WantAPIURL)
			}
		})
	}
}

type corpusCase struct {
	Name           string `json:"name"`
	Input          string `json:"input"`
	WantHost       string `json:"want_host"`
	WantVaultID    string `json:"want_vault_id"`
	WantNormalized string `json:"want_normalized"`
	WantDialURL    string `json:"want_dial_url"`
	WantAPIURL     string `json:"want_api_url"`
	WantError      string `json:"want_error"`
}
