# Changelog

## 1.2.0 - 2026-02-13

### Changed
- Restructured config to use nested YAML keys (`email.*`, `filter.*`, `user`/`pass`)
- `filter.count` is now optional — omit or set to 0 to scan all messages
- Only emails from the current month are matched

## 1.1.0 - 2026-02-13

### Changed
- Simplified and refactored codebase for readability
- Extracted `fetchMatchingUIDs` and `fetchBodies` from monolithic fetch function
- Added doc comments to all functions and types
- Switched from wkhtmltopdf to headless Chrome (chromedp) for PDF rendering

### Added
- Configurable outgoing email subject (`send_subject`)
- Configurable email filter (`filter_subject`, `filter_from`)
- Configurable sender address (`sender`)
- Configurable inbox scan count (`email_count`)

## 1.0.0 - 2026-02-13

### Added
- IMAP inbox scanning with configurable email count
- HTML-to-PDF conversion using headless Chrome (chromedp)
- Images embedded as base64 data URIs for reliable PDF rendering
- HTML cleanup: removes action buttons, help links, and footer link bar
- UID-Nr line rendered bold in footer
- All matching PDFs sent as attachments in a single email via SMTP
- Sample configuration file (`config.yaml.example`)
