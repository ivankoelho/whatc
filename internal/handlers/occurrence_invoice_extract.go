package handlers

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Reading an invoice (NF-e) to PRE-FILL the invoice form. It proposes; a person reviews and
// confirms before anything is saved. It is not a fiscal validation: a well-formed XML with a
// plausible access key is still just a file somebody sent. Nothing here is stored.
//
// Sources, in order of trust:
//   - NF-e XML: structured fields (number, series, date, key, issuer, items).
//   - PDF with a text layer: the access key (it carries the number and series, with a check digit),
//     and the issue date when it can be found. Needs `pdftotext` (poppler-utils) on the server.
//   - Scanned PDF or photo: needs OCR; none is installed, so it says so and the form stays manual.

type invoiceItem struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
}

// InvoiceExtraction is the proposal returned to the form.
type InvoiceExtraction struct {
	Method    string        `json:"method"` // xml | pdf_text | none
	Number    string        `json:"number,omitempty"`
	Series    string        `json:"series,omitempty"`
	IssueDate string        `json:"issue_date,omitempty"` // 2006-01-02
	AccessKey string        `json:"access_key,omitempty"`
	Issuer    string        `json:"issuer,omitempty"`
	Items     []invoiceItem `json:"items"`
	// Uncertain names the fields that were found but not with solid evidence; Warnings are
	// plain facts for the person (what could not be read, and why).
	Uncertain []string `json:"uncertain"`
	Warnings  []string `json:"warnings"`
}

var (
	reDigits44  = regexp.MustCompile(`(?:\d[\s.]*){44}`)
	reDateBR    = regexp.MustCompile(`\b(\d{2})/(\d{2})/(\d{4})\b`)
	nonDigit    = regexp.MustCompile(`\D`)
	reEmissao   = regexp.MustCompile(`(?is)emiss[aã]o.{0,60}?(\d{2}/\d{2}/\d{4})`)
	maxPDFBytes = 15 << 20
)

// accessKeyValid checks the 44 digits and their mod-11 check digit.
func accessKeyValid(k string) bool {
	if len(k) != 44 {
		return false
	}
	sum, weight := 0, 2
	for i := 42; i >= 0; i-- {
		d := int(k[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		sum += d * weight
		if weight++; weight > 9 {
			weight = 2
		}
	}
	dv := 11 - sum%11
	if dv >= 10 {
		dv = 0
	}
	return int(k[43]-'0') == dv
}

// fromAccessKey: series and number are positions 22-24 and 25-33; AAMM at 2-5.
func fromAccessKey(k string) (series, number string) {
	s, _ := strconv.Atoi(k[22:25])
	n, _ := strconv.Atoi(k[25:34])
	return strconv.Itoa(s), strconv.Itoa(n)
}

type nfeXML struct {
	Info struct {
		ID  string `xml:"Id,attr"`
		Ide struct {
			Number string `xml:"nNF"`
			Series string `xml:"serie"`
			DhEmi  string `xml:"dhEmi"`
			DEmi   string `xml:"dEmi"`
		} `xml:"ide"`
		Emit struct {
			Name string `xml:"xNome"`
		} `xml:"emit"`
		Det []struct {
			Prod struct {
				Code string `xml:"cProd"`
				Name string `xml:"xProd"`
				Qty  string `xml:"qCom"`
				Unit string `xml:"uCom"`
			} `xml:"prod"`
		} `xml:"det"`
	} `xml:"infNFe"`
}

// parseNFeXML extracts the fields of an NF-e (nfeProc or bare NFe) without resolving anything external.
func parseNFeXML(data []byte) (InvoiceExtraction, bool) {
	out := InvoiceExtraction{Method: "xml", Items: []invoiceItem{}, Uncertain: []string{}, Warnings: []string{}}
	if err := checkPlainXML(data); err != nil {
		return out, false
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var found *nfeXML
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "NFe" {
			var n struct {
				nfeXML
			}
			if dec.DecodeElement(&n, &se) == nil {
				found = &n.nfeXML
			}
			break
		}
	}
	if found == nil || found.Info.Ide.Number == "" {
		return out, false
	}
	in := found.Info
	out.Number = strings.TrimLeft(strings.TrimSpace(in.Ide.Number), "0")
	out.Series = strings.TrimSpace(in.Ide.Series)
	out.Issuer = strings.TrimSpace(in.Emit.Name)
	if key := strings.TrimPrefix(in.ID, "NFe"); len(key) == 44 && nonDigit.ReplaceAllString(key, "") == key {
		out.AccessKey = key
		if !accessKeyValid(key) {
			out.Uncertain = append(out.Uncertain, "access_key")
			out.Warnings = append(out.Warnings, "access_key_check_digit")
		}
	}
	raw := in.Ide.DhEmi
	if raw == "" {
		raw = in.Ide.DEmi
	}
	if len(raw) >= 10 {
		if _, err := time.Parse("2006-01-02", raw[:10]); err == nil {
			out.IssueDate = raw[:10]
		}
	}
	for _, d := range in.Det {
		desc := strings.TrimSpace(d.Prod.Name)
		if desc == "" {
			continue
		}
		qty := strings.TrimSpace(d.Prod.Qty)
		if q, err := strconv.ParseFloat(qty, 64); err == nil {
			qty = strconv.FormatFloat(q, 'f', -1, 64)
		}
		if u := strings.TrimSpace(d.Prod.Unit); u != "" && qty != "" {
			qty += " " + u
		}
		out.Items = append(out.Items, invoiceItem{Code: strings.TrimSpace(d.Prod.Code), Description: desc, Quantity: qty})
		if len(out.Items) >= maxOccurrenceDocumentItems {
			out.Warnings = append(out.Warnings, "items_truncated")
			break
		}
	}
	if out.Number == "" {
		return out, false
	}
	return out, true
}

// parseDanfeText finds what a PDF's text layer reliably carries: the access key (and from it the
// number and series) and, if present, the issue date. Items are left to the person.
func parseDanfeText(text string) InvoiceExtraction {
	out := InvoiceExtraction{Method: "pdf_text", Items: []invoiceItem{}, Uncertain: []string{}, Warnings: []string{}}
	for _, m := range reDigits44.FindAllString(text, -1) {
		k := nonDigit.ReplaceAllString(m, "")
		if len(k) == 44 && accessKeyValid(k) {
			out.AccessKey = k
			out.Series, out.Number = fromAccessKey(k)
			break
		}
	}
	if out.AccessKey == "" {
		out.Warnings = append(out.Warnings, "no_access_key")
	}
	date := ""
	if m := reEmissao.FindStringSubmatch(text); m != nil {
		date = m[1]
	} else if m := reDateBR.FindStringSubmatch(text); m != nil {
		date = m[0]
		out.Uncertain = append(out.Uncertain, "issue_date") // the first date on the page is a guess
	}
	if date != "" {
		if t, err := time.Parse("02/01/2006", date); err == nil {
			out.IssueDate = t.Format("2006-01-02")
		}
	}
	out.Warnings = append(out.Warnings, "items_not_read")
	return out
}

// pdfText returns the text layer, "" with ok=false when pdftotext is not installed.
func pdfText(data []byte) (text string, available bool) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tmp, err := os.CreateTemp("", "nf-*.pdf")
	if err != nil {
		return "", true
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, _ = tmp.Write(data)
	_ = tmp.Close()
	cmd := exec.CommandContext(ctx, bin, "-q", "-layout", "-l", "3", tmp.Name(), "-")
	out, err := cmd.Output()
	if err != nil {
		return "", true
	}
	if len(out) > 1<<20 {
		out = out[:1<<20]
	}
	return string(out), true
}

// ExtractOccurrenceInvoice reads an uploaded invoice and PROPOSES form values. The caller must be
// able to create invoice records on the case; the file is neither stored nor kept.
func (a *App) ExtractOccurrenceInvoice(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	if _, _, err := a.loadOccurrenceWithAccess(r, orgID, userID, true); err != nil {
		return nil
	}
	if !a.chatUploadAllowed(userID) {
		return a.sendChatError(r, errChatRateLimited, "read the invoice")
	}
	form, ferr := r.RequestCtx.MultipartForm()
	if ferr != nil || len(form.File["file"]) == 0 {
		return a.sendChatError(r, errOccFileInvalid, "read the invoice")
	}
	fh := form.File["file"][0]
	if fh.Size > int64(maxPDFBytes) {
		return a.sendChatError(r, errOccFileTooLarge, "read the invoice")
	}
	f, oerr := fh.Open()
	if oerr != nil {
		return a.sendChatError(r, errOccFileInvalid, "read the invoice")
	}
	defer func() { _ = f.Close() }()
	data, rerr := io.ReadAll(io.LimitReader(f, int64(maxPDFBytes)+1))
	if rerr != nil || len(data) > maxPDFBytes {
		return a.sendChatError(r, errOccFileTooLarge, "read the invoice")
	}
	info, ierr := inspectOccurrenceFile(data, fh.Filename)
	if ierr != nil {
		return a.sendChatError(r, ierr, "read the invoice")
	}

	none := InvoiceExtraction{Method: "none", Items: []invoiceItem{}, Uncertain: []string{}, Warnings: []string{}}
	switch {
	case info.Mime == "application/xml":
		res, ok := parseNFeXML(data)
		if !ok {
			none.Warnings = append(none.Warnings, "not_an_nfe")
			return r.SendEnvelope(none)
		}
		return r.SendEnvelope(res)
	case info.Mime == "application/pdf":
		text, avail := pdfText(data)
		if !avail {
			none.Warnings = append(none.Warnings, "pdf_reader_unavailable")
			return r.SendEnvelope(none)
		}
		if len(strings.Fields(text)) < 20 {
			none.Warnings = append(none.Warnings, "scanned_needs_ocr")
			return r.SendEnvelope(none)
		}
		return r.SendEnvelope(parseDanfeText(text))
	case info.Kind == "image":
		none.Warnings = append(none.Warnings, "scanned_needs_ocr")
		return r.SendEnvelope(none)
	}
	return r.SendErrorEnvelope(fasthttp.StatusUnsupportedMediaType, "Only XML, PDF or images can be read", nil, "")
}
