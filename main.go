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

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"gopkg.in/gomail.v2"
	"gopkg.in/yaml.v3"
)

type Config struct {
	IMAP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"imap"`
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"smtp"`
	Email        string `yaml:"email"`
	Password     string `yaml:"password"`
	Recipient    string `yaml:"recipient"`
	Sender       string `yaml:"sender"`
	EmailCount   int    `yaml:"email_count"`
	FilterSubject string `yaml:"filter_subject"`
	FilterFrom   string `yaml:"filter_from"`
	SendSubject  string `yaml:"send_subject"`
}

type InvoiceEmail struct {
	Subject  string
	HTMLBody string
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	if cfg.EmailCount <= 0 {
		cfg.EmailCount = 10
	}
	if cfg.Sender == "" {
		cfg.Sender = cfg.Email
	}
	if cfg.FilterSubject == "" {
		cfg.FilterSubject = "Deine Rechnung von Apple"
	}
	if cfg.FilterFrom == "" {
		cfg.FilterFrom = "apple.com"
	}
	if cfg.SendSubject == "" {
		cfg.SendSubject = "Deine PDF-Rechnungen von Apple"
	}
	return &cfg, nil
}

func matchesFilter(env *imap.Envelope, cfg *Config) bool {
	if env.Subject != cfg.FilterSubject {
		return false
	}
	for _, addr := range env.From {
		if strings.Contains(strings.ToLower(addr.HostName), strings.ToLower(cfg.FilterFrom)) {
			return true
		}
	}
	return false
}

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

		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/html" {
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

func fetchInvoices(cfg *Config) ([]InvoiceEmail, error) {
	addr := fmt.Sprintf("%s:%d", cfg.IMAP.Host, cfg.IMAP.Port)
	c, err := client.DialTLS(addr, &tls.Config{ServerName: cfg.IMAP.Host})
	if err != nil {
		return nil, fmt.Errorf("connecting to IMAP server: %w", err)
	}
	defer c.Logout()

	if err := c.Login(cfg.Email, cfg.Password); err != nil {
		return nil, fmt.Errorf("IMAP login: %w", err)
	}
	log.Println("Logged in to IMAP server")

	mbox, err := c.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("selecting INBOX: %w", err)
	}
	log.Printf("INBOX has %d messages", mbox.Messages)

	if mbox.Messages == 0 {
		return nil, nil
	}

	// Determine range for last N messages
	count := uint32(cfg.EmailCount)
	from := uint32(1)
	if mbox.Messages > count {
		from = mbox.Messages - count + 1
	}
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	// Pass 1: Fetch envelopes only
	envelopeItems := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}
	envelopeChan := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqSet, envelopeItems, envelopeChan)
	}()

	var matchUIDs []uint32
	for msg := range envelopeChan {
		if msg.Envelope != nil && matchesFilter(msg.Envelope, cfg) {
			log.Printf("Found invoice: %q (UID %d)", msg.Envelope.Subject, msg.Uid)
			matchUIDs = append(matchUIDs, msg.Uid)
		}
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetching envelopes: %w", err)
	}

	if len(matchUIDs) == 0 {
		log.Println("No invoice emails found")
		return nil, nil
	}
	log.Printf("Found %d invoice(s), fetching bodies...", len(matchUIDs))

	// Pass 2: Fetch full bodies for matching UIDs
	uidSet := new(imap.SeqSet)
	for _, uid := range matchUIDs {
		uidSet.AddNum(uid)
	}

	section := &imap.BodySectionName{Peek: true}
	bodyItems := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}
	bodyChan := make(chan *imap.Message, len(matchUIDs))
	done = make(chan error, 1)
	go func() {
		done <- c.UidFetch(uidSet, bodyItems, bodyChan)
	}()

	var invoices []InvoiceEmail
	for msg := range bodyChan {
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

		invoices = append(invoices, InvoiceEmail{
			Subject:  msg.Envelope.Subject,
			HTMLBody: htmlBody,
		})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("fetching bodies: %w", err)
	}

	return invoices, nil
}

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
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func cleanHTML(htmlContent string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}

	// Embed images as base64 data URIs to preserve them in PDF
	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		src, exists := s.Attr("src")
		if exists && strings.HasPrefix(src, "http") {
			if dataURI, err := embedImage(src); err == nil {
				s.SetAttr("src", dataURI)
			}
		}
	})

	// Remove the blue button and the paragraph above it
	doc.Find(".action-button-cell").Remove()
	doc.Find("#footer_section > p").First().Remove()

	// Remove "Hilfe bei Abos und Käufen" block
	doc.Find("#footer_section > .custom-1sstyyn").Remove()

	// Make UID-Nr line bold
	doc.Find(".footer-copy p").Each(func(_ int, s *goquery.Selection) {
		if strings.Contains(s.Text(), "UID-Nr") {
			s.SetAttr("style", "font-weight:600")
		}
	})

	// Remove last footer line with links
	doc.Find(".inline-link-group").Remove()

	html, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("rendering HTML: %w", err)
	}
	return html, nil
}

func convertHTMLToPDF(htmlContent string) ([]byte, error) {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	var pdfBuf []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			frameTree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			return page.SetDocumentContent(frameTree.Frame.ID, htmlContent).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			pdfBuf, _, err = page.PrintToPDF().
				WithPaperWidth(8.27).   // A4 width in inches
				WithPaperHeight(11.69). // A4 height in inches
				WithPrintBackground(true).
				Do(ctx)
			return err
		}),
	); err != nil {
		return nil, fmt.Errorf("generating PDF: %w", err)
	}

	return pdfBuf, nil
}

func sanitizeFilename(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9äöüÄÖÜß\-_ ]+`)
	s = re.ReplaceAllString(s, "_")
	s = strings.TrimSpace(s)
	if s == "" {
		s = "invoice"
	}
	return s
}

type PDFAttachment struct {
	Filename string
	Data     []byte
}

func sendPDFEmail(cfg *Config, attachments []PDFAttachment) error {
	m := gomail.NewMessage()
	m.SetHeader("From", cfg.Sender)
	m.SetHeader("To", cfg.Recipient)
	m.SetHeader("Subject", cfg.SendSubject)
	m.SetBody("text/plain", "Anbei Deine Rechnungen:\n\n")

	for _, att := range attachments {
		data := att.Data // capture for closure
		m.Attach(att.Filename, gomail.SetCopyFunc(func(w io.Writer) error {
			_, err := io.Copy(w, bytes.NewReader(data))
			return err
		}))
	}

	d := gomail.NewDialer(cfg.SMTP.Host, cfg.SMTP.Port, cfg.Email, cfg.Password)
	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	return nil
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

	log.Printf("Processing %d invoice(s)...", len(invoices))
	var attachments []PDFAttachment
	for i, inv := range invoices {
		log.Printf("[%d/%d] Converting %q to PDF...", i+1, len(invoices), inv.Subject)

		cleanedHTML, err := cleanHTML(inv.HTMLBody)
		if err != nil {
			log.Printf("ERROR cleaning HTML: %v", err)
			continue
		}

		pdfData, err := convertHTMLToPDF(cleanedHTML)
		if err != nil {
			log.Printf("ERROR converting to PDF: %v", err)
			continue
		}
		log.Printf("[%d/%d] PDF generated (%d bytes)", i+1, len(invoices), len(pdfData))

		filename := sanitizeFilename(inv.Subject)
		if len(invoices) > 1 {
			filename = fmt.Sprintf("%s_%d", filename, i+1)
		}
		attachments = append(attachments, PDFAttachment{
			Filename: filename + ".pdf",
			Data:     pdfData,
		})
	}

	if len(attachments) == 0 {
		log.Println("No PDFs generated")
		return
	}

	log.Printf("Sending email with %d PDF attachment(s)...", len(attachments))
	if err := sendPDFEmail(cfg, attachments); err != nil {
		log.Fatalf("ERROR sending email: %v", err)
	}
	log.Printf("Email with %d PDF(s) sent to %s", len(attachments), cfg.Recipient)
	log.Println("Done")
}
