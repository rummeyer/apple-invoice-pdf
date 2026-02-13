package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

// --- loadConfig tests ---

func TestLoadConfig_Full(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
imap:
  host: imap.example.com
  port: 993
smtp:
  host: smtp.example.com
  port: 587
user: user@example.com
pass: secret
email:
  from: sender@example.com
  to: recipient@example.com
  subject: Custom Subject
filter:
  count: 50
  subject: My Invoice
  from: example.com
`), 0644)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IMAP.Host != "imap.example.com" {
		t.Errorf("IMAP.Host = %q, want %q", cfg.IMAP.Host, "imap.example.com")
	}
	if cfg.IMAP.Port != 993 {
		t.Errorf("IMAP.Port = %d, want %d", cfg.IMAP.Port, 993)
	}
	if cfg.SMTP.Host != "smtp.example.com" {
		t.Errorf("SMTP.Host = %q, want %q", cfg.SMTP.Host, "smtp.example.com")
	}
	if cfg.Email.From != "sender@example.com" {
		t.Errorf("Email.From = %q, want %q", cfg.Email.From, "sender@example.com")
	}
	if cfg.Email.Subject != "Custom Subject" {
		t.Errorf("Email.Subject = %q, want %q", cfg.Email.Subject, "Custom Subject")
	}
	if cfg.Filter.Count != 50 {
		t.Errorf("Filter.Count = %d, want %d", cfg.Filter.Count, 50)
	}
	if cfg.Filter.Subject != "My Invoice" {
		t.Errorf("Filter.Subject = %q, want %q", cfg.Filter.Subject, "My Invoice")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
user: user@example.com
pass: secret
`), 0644)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Email.From != "user@example.com" {
		t.Errorf("Email.From default = %q, want %q", cfg.Email.From, "user@example.com")
	}
	if cfg.Filter.Subject != "Deine Rechnung von Apple" {
		t.Errorf("Filter.Subject default = %q, want %q", cfg.Filter.Subject, "Deine Rechnung von Apple")
	}
	if cfg.Filter.From != "apple.com" {
		t.Errorf("Filter.From default = %q, want %q", cfg.Filter.From, "apple.com")
	}
	if cfg.Email.Subject != "Deine PDF-Rechnungen von Apple" {
		t.Errorf("Email.Subject default = %q, want %q", cfg.Email.Subject, "Deine PDF-Rechnungen von Apple")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`{{{invalid`), 0644)

	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// --- matchesFilter tests ---

func makeEnvelope(subject string, hostname string, date time.Time) *imap.Envelope {
	return &imap.Envelope{
		Subject: subject,
		Date:    date,
		From: []*imap.Address{
			{HostName: hostname},
		},
	}
}

func defaultCfg() *Config {
	cfg := &Config{}
	cfg.Filter.Subject = "Deine Rechnung von Apple"
	cfg.Filter.From = "apple.com"
	return cfg
}

func TestMatchesFilter_Match(t *testing.T) {
	cfg := defaultCfg()
	env := makeEnvelope("Deine Rechnung von Apple", "email.apple.com", time.Now())
	if !matchesFilter(env, cfg) {
		t.Error("expected match")
	}
}

func TestMatchesFilter_WrongSubject(t *testing.T) {
	cfg := defaultCfg()
	env := makeEnvelope("Other Subject", "email.apple.com", time.Now())
	if matchesFilter(env, cfg) {
		t.Error("expected no match for wrong subject")
	}
}

func TestMatchesFilter_WrongSender(t *testing.T) {
	cfg := defaultCfg()
	env := makeEnvelope("Deine Rechnung von Apple", "other.com", time.Now())
	if matchesFilter(env, cfg) {
		t.Error("expected no match for wrong sender domain")
	}
}

func TestMatchesFilter_OldMonth(t *testing.T) {
	cfg := defaultCfg()
	oldDate := time.Now().AddDate(0, -2, 0)
	env := makeEnvelope("Deine Rechnung von Apple", "email.apple.com", oldDate)
	if matchesFilter(env, cfg) {
		t.Error("expected no match for old month")
	}
}

func TestMatchesFilter_CaseInsensitiveDomain(t *testing.T) {
	cfg := defaultCfg()
	env := makeEnvelope("Deine Rechnung von Apple", "Email.APPLE.COM", time.Now())
	if !matchesFilter(env, cfg) {
		t.Error("expected case-insensitive domain match")
	}
}

func TestMatchesFilter_NoFromAddresses(t *testing.T) {
	cfg := defaultCfg()
	env := &imap.Envelope{
		Subject: "Deine Rechnung von Apple",
		Date:    time.Now(),
		From:    []*imap.Address{},
	}
	if matchesFilter(env, cfg) {
		t.Error("expected no match with empty From")
	}
}

// --- sanitizeFilename tests ---

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal text", "Deine Rechnung von Apple", "Deine Rechnung von Apple"},
		{"special chars", "Invoice #123 (2024)", "Invoice _123 _2024_"},
		{"umlauts preserved", "Rechnungsübersicht für März", "Rechnungsübersicht für März"},
		{"only special chars", "!!!", "_"},
		{"empty string", "", "invoice"},
		{"hyphens and underscores", "my-file_name", "my-file_name"},
		{"slashes removed", "path/to/file", "path_to_file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- cleanHTML tests ---

func TestCleanHTML_RemovesActionButton(t *testing.T) {
	html := `<html><body>
		<div class="action-button-cell">Click here</div>
		<p>Keep this</p>
	</body></html>`

	result, err := cleanHTML(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(result, "action-button-cell") {
		t.Error("expected action-button-cell to be removed")
	}
	if !contains(result, "Keep this") {
		t.Error("expected other content to be preserved")
	}
}

func TestCleanHTML_RemovesInlineLinkGroup(t *testing.T) {
	html := `<html><body>
		<div class="inline-link-group">Privacy | Terms</div>
		<p>Content</p>
	</body></html>`

	result, err := cleanHTML(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(result, "inline-link-group") {
		t.Error("expected inline-link-group to be removed")
	}
}

func TestCleanHTML_BoldsUIDNr(t *testing.T) {
	html := `<html><body>
		<div class="footer-copy"><p>UID-Nr: ATU12345</p></div>
	</body></html>`

	result, err := cleanHTML(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "font-weight:600") {
		t.Error("expected UID-Nr paragraph to be bolded")
	}
}

func TestCleanHTML_PreservesNonImageContent(t *testing.T) {
	html := `<html><body>
		<h1>Invoice</h1>
		<p>Amount: €9.99</p>
	</body></html>`

	result, err := cleanHTML(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "Invoice") || !contains(result, "€9.99") {
		t.Error("expected content to be preserved")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsSubstring(s, substr)
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
