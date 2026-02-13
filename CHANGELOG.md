# Changelog

## 1.0.0 - 2026-02-13

### Added
- IMAP inbox scanning with configurable email count
- Configurable subject and sender domain filtering
- HTML-to-PDF conversion using headless Chrome (chromedp)
- Images embedded as base64 data URIs for reliable PDF rendering
- HTML cleanup: removes action buttons, help links, and footer link bar
- UID-Nr line rendered bold in footer
- All matching PDFs sent as attachments in a single email via SMTP
- Configurable sender, recipient, and outgoing email subject
- Sample configuration file (`config.yaml.example`)
