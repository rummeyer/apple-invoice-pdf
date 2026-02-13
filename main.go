// apple-invoice-pdf fetches invoice emails from an IMAP inbox,
// converts their HTML body to PDF using headless Chrome,
// and sends all PDFs as attachments in a single email via SMTP.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"gopkg.in/gomail.v2"
	"gopkg.in/yaml.v3"
)

// Config holds all settings loaded from config.yaml.
type Config struct {
	IMAP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"imap"`
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"smtp"`
	User  string `yaml:"user"`
	Pass  string `yaml:"pass"`
	Email struct {
		From    string `yaml:"from"`
		To      string `yaml:"to"`
		Subject string `yaml:"subject"`
	} `yaml:"email"`
	Filter struct {
		Count   int    `yaml:"count"`
		Subject string `yaml:"subject"`
		From    string `yaml:"from"`
	} `yaml:"filter"`
}

// InvoiceEmail holds a matched email's subject, date, and HTML content.
type InvoiceEmail struct {
	Subject  string
	Date     time.Time
	HTMLBody string
}

// PDFAttachment holds a generated PDF ready for email attachment.
type PDFAttachment struct {
	Filename string
	Data     []byte
}

// loadConfig reads config.yaml and applies defaults for optional fields.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	if cfg.Email.From == "" {
		cfg.Email.From = cfg.User
	}
	if cfg.Filter.Subject == "" {
		cfg.Filter.Subject = "Deine Rechnung von Apple"
	}
	if cfg.Filter.From == "" {
		cfg.Filter.From = "apple.com"
	}
	if cfg.Email.Subject == "" {
		cfg.Email.Subject = "Deine PDF-Rechnungen von Apple"
	}
	return &cfg, nil
}

// matchesFilter checks if an email envelope matches the configured subject,
// sender domain, and is from the current month.
func matchesFilter(env *imap.Envelope, cfg *Config) bool {
	// Only match emails from the current month
	now := time.Now()
	if env.Date.Year() != now.Year() || env.Date.Month() != now.Month() {
		return false
	}
	if env.Subject != cfg.Filter.Subject {
		return false
	}
	for _, addr := range env.From {
		if strings.Contains(strings.ToLower(addr.HostName), strings.ToLower(cfg.Filter.From)) {
			return true
		}
	}
	return false
}

// extractHTMLBody walks MIME parts and returns the first text/html content.
func extractHTMLBody(r io.Reader) (string, error) {
	mr, err := mail.CreateReader(r)
	if err != nil {
		return "", fmt.Errorf("creating mail reader: %w", err)
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading mail part: %w", err)
		}
		if h, ok := p.Header.(*mail.InlineHeader); ok {
			if ct, _, _ := h.ContentType(); ct == "text/html" {
				body, err := io.ReadAll(p.Body)
				if err != nil {
					return "", fmt.Errorf("reading HTML body: %w", err)
				}
				return string(body), nil
			}
		}
	}
	return "", fmt.Errorf("no text/html part found")
}

// fetchInvoices connects to IMAP, scans the last N emails, and returns
// matching invoices. Uses a two-pass approach: first fetch lightweight
// envelopes, then fetch full bodies only for matches.
func fetchInvoices(cfg *Config) ([]InvoiceEmail, error) {
	// Connect via TLS
	addr := fmt.Sprintf("%s:%d", cfg.IMAP.Host, cfg.IMAP.Port)
	c, err := client.DialTLS(addr, &tls.Config{ServerName: cfg.IMAP.Host})
	if err != nil {
		return nil, fmt.Errorf("connecting to IMAP server: %w", err)
	}
	defer c.Logout()

	if err := c.Login(cfg.User, cfg.Pass); err != nil {
		return nil, fmt.Errorf("IMAP login: %w", err)
	}
	log.Println("Logged in to IMAP server")

	// Open INBOX read-only (true) since we never modify messages
	mbox, err := c.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("selecting INBOX: %w", err)
	}
	log.Printf("INBOX has %d messages", mbox.Messages)
	if mbox.Messages == 0 {
		return nil, nil
	}

	// Build sequence set: last N messages if count is set, otherwise all
	from := uint32(1)
	if cfg.Filter.Count > 0 {
		count := uint32(cfg.Filter.Count)
		if mbox.Messages > count {
			from = mbox.Messages - count + 1
		}
	}
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	// Pass 1: fetch envelopes only (lightweight) to find matches
	matchUIDs := fetchMatchingUIDs(c, seqSet, cfg)
	if len(matchUIDs) == 0 {
		log.Println("No invoice emails found")
		return nil, nil
	}
	log.Printf("Found %d invoice(s), fetching bodies...", len(matchUIDs))

	// Pass 2: fetch full bodies only for matching UIDs (Peek=true to avoid marking as read)
	return fetchBodies(c, matchUIDs)
}

// fetchMatchingUIDs fetches envelopes and returns UIDs of emails matching the filter.
func fetchMatchingUIDs(c *client.Client, seqSet *imap.SeqSet, cfg *Config) []uint32 {
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(seqSet, items, messages) }()

	var uids []uint32
	for msg := range messages {
		if msg.Envelope != nil && matchesFilter(msg.Envelope, cfg) {
			log.Printf("Found invoice: %q (UID %d)", msg.Envelope.Subject, msg.Uid)
			uids = append(uids, msg.Uid)
		}
	}
	if err := <-done; err != nil {
		log.Printf("WARNING: fetching envelopes: %v", err)
	}
	return uids
}

// fetchBodies fetches full MIME bodies for the given UIDs and extracts HTML content.
func fetchBodies(c *client.Client, uids []uint32) ([]InvoiceEmail, error) {
	uidSet := new(imap.SeqSet)
	for _, uid := range uids {
		uidSet.AddNum(uid)
	}

	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}
	messages := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(uidSet, items, messages) }()

	var invoices []InvoiceEmail
	for msg := range messages {
		r := msg.GetBody(section)
		if r == nil {
			log.Printf("WARNING: no body for UID %d", msg.Uid)
			continue
		}
		htmlBody, err := extractHTMLBody(r)
		if err != nil {
			log.Printf("WARNING: extracting HTML from UID %d: %v", msg.Uid, err)
			continue
		}
		invoices = append(invoices, InvoiceEmail{Subject: msg.Envelope.Subject, Date: msg.Envelope.Date, HTMLBody: htmlBody})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetching bodies: %w", err)
	}
	return invoices, nil
}

// embedImage downloads an image URL and returns it as a base64 data URI.
func embedImage(imgURL string) (string, error) {
	resp, err := http.Get(imgURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

// cleanHTML removes unwanted elements from the invoice HTML and embeds
// external images as base64 so they render reliably in the PDF.
func cleanHTML(htmlContent string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}

	// Embed external images as base64 data URIs
	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok && strings.HasPrefix(src, "http") {
			if dataURI, err := embedImage(src); err == nil {
				s.SetAttr("src", dataURI)
			}
		}
	})

	// Remove action button and its intro paragraph
	doc.Find(".action-button-cell").Remove()
	doc.Find("#footer_section > p").First().Remove()

	// Remove help links section
	doc.Find("#footer_section > .custom-1sstyyn").Remove()

	// Bold the UID-Nr line in footer
	doc.Find(".footer-copy p").Each(func(_ int, s *goquery.Selection) {
		if strings.Contains(s.Text(), "UID-Nr") {
			s.SetAttr("style", "font-weight:600")
		}
	})

	// Remove bottom link bar (privacy, terms, etc.)
	doc.Find(".inline-link-group").Remove()

	html, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("rendering HTML: %w", err)
	}
	return html, nil
}

// extractOrderNumber parses the invoice HTML for the value following
// the "Bestellnummer:" label and returns it (trimmed). Returns an empty
// string if no order number is found.
func extractOrderNumber(htmlContent string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return ""
	}
	var orderNum string
	doc.Find("*").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		text := strings.TrimSpace(s.Text())
		if strings.HasPrefix(text, "Bestellnummer:") {
			orderNum = strings.TrimSpace(strings.TrimPrefix(text, "Bestellnummer:"))
			// Take only the first line/word to avoid capturing trailing content
			if idx := strings.IndexAny(orderNum, "\n\r\t"); idx >= 0 {
				orderNum = strings.TrimSpace(orderNum[:idx])
			}
			return false
		}
		return true
	})
	return orderNum
}

// convertHTMLToPDF renders HTML to an A4 PDF using headless Chrome.
func convertHTMLToPDF(htmlContent string) ([]byte, error) {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	var buf []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate("about:blank"),
		// Inject HTML into the page
		chromedp.ActionFunc(func(ctx context.Context) error {
			ft, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			return page.SetDocumentContent(ft.Frame.ID, htmlContent).Do(ctx)
		}),
		// Print to PDF with A4 dimensions
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, _, err = page.PrintToPDF().
				WithPaperWidth(8.27).
				WithPaperHeight(11.69).
				WithPrintBackground(true).
				Do(ctx)
			return err
		}),
	); err != nil {
		return nil, fmt.Errorf("generating PDF: %w", err)
	}
	return buf, nil
}

// sanitizeFilename replaces non-alphanumeric characters for safe filenames.
func sanitizeFilename(s string) string {
	s = regexp.MustCompile(`[^a-zA-Z0-9äöüÄÖÜß\-_ ]+`).ReplaceAllString(s, "_")
	if s = strings.TrimSpace(s); s == "" {
		s = "invoice"
	}
	return s
}

// sendPDFEmail sends a single email with all PDF attachments.
func sendPDFEmail(cfg *Config, attachments []PDFAttachment) error {
	m := gomail.NewMessage()
	m.SetHeader("From", cfg.Email.From)
	m.SetHeader("To", cfg.Email.To)
	m.SetHeader("Subject", cfg.Email.Subject)
	m.SetBody("text/plain", "Dokumente anbei.\n")

	for _, att := range attachments {
		data := att.Data
		m.Attach(att.Filename, gomail.SetCopyFunc(func(w io.Writer) error {
			_, err := io.Copy(w, bytes.NewReader(data))
			return err
		}))
	}

	d := gomail.NewDialer(cfg.SMTP.Host, cfg.SMTP.Port, cfg.User, cfg.Pass)
	return d.DialAndSend(m)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	invoices, err := fetchInvoices(cfg)
	if err != nil {
		log.Fatalf("Failed to fetch invoices: %v", err)
	}
	if len(invoices) == 0 {
		log.Println("No invoices to process")
		return
	}

	// Convert each invoice HTML to PDF
	log.Printf("Processing %d invoice(s)...", len(invoices))
	var attachments []PDFAttachment
	for i, inv := range invoices {
		log.Printf("[%d/%d] Converting %q to PDF...", i+1, len(invoices), inv.Subject)

		cleaned, err := cleanHTML(inv.HTMLBody)
		if err != nil {
			log.Printf("ERROR cleaning HTML: %v", err)
			continue
		}
		pdf, err := convertHTMLToPDF(cleaned)
		if err != nil {
			log.Printf("ERROR converting to PDF: %v", err)
			continue
		}
		log.Printf("[%d/%d] PDF generated (%d bytes)", i+1, len(invoices), len(pdf))

		orderNum := extractOrderNumber(inv.HTMLBody)
		log.Printf("[%d/%d] Extracted order number: %q", i+1, len(invoices), orderNum)
		var filename string
		if orderNum != "" {
			filename = fmt.Sprintf("%02d_%04d_Rechnung_Apple_%s",
				inv.Date.Month(), inv.Date.Year(), sanitizeFilename(orderNum))
		} else {
			filename = sanitizeFilename(inv.Subject)
			if len(invoices) > 1 {
				filename = fmt.Sprintf("%s_%d", filename, i+1)
			}
		}
		attachments = append(attachments, PDFAttachment{Filename: filename + ".pdf", Data: pdf})
	}

	if len(attachments) == 0 {
		log.Println("No PDFs generated")
		return
	}

	// Send all PDFs in a single email
	log.Printf("Sending email with %d PDF attachment(s)...", len(attachments))
	if err := sendPDFEmail(cfg, attachments); err != nil {
		log.Fatalf("ERROR sending email: %v", err)
	}
	log.Printf("Email with %d PDF(s) sent to %s", len(attachments), cfg.Email.To)
}
