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
email: "user@example.com"
password: "app-specific-password"

sender: "sender@example.com"
recipient: "recipient@example.com"
send_subject: "Deine PDF-Rechnungen von Apple"

email_count: 10
filter_subject: "Deine Rechnung von Apple"
filter_from: "apple.com"
```

| Field | Description | Default |
|---|---|---|
| `email_count` | Number of recent emails to scan | `10` |
| `filter_subject` | Exact subject line to match | `Deine Rechnung von Apple` |
| `filter_from` | Sender domain to match | `apple.com` |
| `sender` | From address for outgoing email | same as `email` |
| `send_subject` | Subject line for outgoing email | `Deine PDF-Rechnungen von Apple` |

## Usage

```bash
./apple-invoice-pdf
```

The tool will:

1. Connect to the IMAP server and scan the last N emails
2. Filter by configured subject and sender domain
3. Extract the HTML body and convert each to an A4 PDF
4. Send all PDFs as attachments in a single email to the configured recipient

## License

MIT
