# apple-invoice-pdf

A Go CLI tool that fetches invoice emails from an IMAP inbox, converts their HTML body to PDF, and sends the PDFs as attachments via SMTP.

## Prerequisites

- Go 1.21+
- Google Chrome or Chromium (used headlessly for HTML-to-PDF conversion)

## Installation

```bash
go build -o apple-invoice-pdf .
```

## Configuration

Copy the sample config and fill in your credentials:

```bash
cp config.yaml.example config.yaml
```

```yaml
imap:
  host: "imap.example.com"
  port: 993
smtp:
  host: "smtp.example.com"
  port: 587

user: "user@example.com"
pass: "app-specific-password"

email:
  from: "sender@example.com"
  to: "recipient@example.com"
  subject: "Deine PDF-Rechnungen von Apple"

filter:
  count: 10
  subject: "Deine Rechnung von Apple"
  from: "apple.com"
```

| Field | Description | Default |
|---|---|---|
| `filter.count` | Number of recent emails to scan (0 or omit for no limit) | none (all) |
| `filter.subject` | Exact subject line to match | `Deine Rechnung von Apple` |
| `filter.from` | Sender domain to match | `apple.com` |
| `email.from` | From address for outgoing email | same as `user` |
| `email.subject` | Subject line for outgoing email | `Deine PDF-Rechnungen von Apple` |

## Usage

```bash
./apple-invoice-pdf
```

The tool will:

1. Connect to the IMAP server and scan the last N emails (or all if count is omitted)
2. Filter by configured subject, sender domain, and current month
3. Extract the HTML body and convert each to an A4 PDF
4. Name each PDF as `MM_YYYY_Rechnung_Apple_BESTELLNUMMER.pdf` using the order number from the invoice (falls back to subject-based naming if not found)
5. Send all PDFs as attachments in a single email to the configured recipient

## License

MIT
