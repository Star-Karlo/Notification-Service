package grpcserver

import (
	"fmt"
	"html"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
)

// documentEmailTemplate is one mail layout.
type documentEmailTemplate struct {
	Subject map[string]string
	Heading map[string]string
	Intro   map[string]string
}

// documentEmailTemplates holds the document mail copy. As with push
// notifications, the copy lives here rather than at the call sites, so the same
// event reads the same way whichever service raised it.
var documentEmailTemplates = map[string]documentEmailTemplate{
	"invoice_issued": {
		Subject: map[string]string{"id": "Invoice %s", "en": "Invoice %s"},
		Heading: map[string]string{"id": "Invoice diterbitkan", "en": "Invoice issued"},
		Intro: map[string]string{
			"id": "Berikut invoice %s dengan total %s. Dokumen terlampir.",
			"en": "Here is invoice %s for %s. The document is attached.",
		},
	},
	"order_confirmation": {
		Subject: map[string]string{"id": "Konfirmasi order %s", "en": "Order confirmation %s"},
		Heading: map[string]string{"id": "Order dikonfirmasi", "en": "Order confirmed"},
		Intro: map[string]string{
			"id": "Order %s telah dikonfirmasi.",
			"en": "Order %s has been confirmed.",
		},
	},
	"proof_of_delivery": {
		Subject: map[string]string{"id": "Bukti pengiriman %s", "en": "Proof of delivery %s"},
		Heading: map[string]string{"id": "Bukti pengiriman", "en": "Proof of delivery"},
		Intro: map[string]string{
			"id": "Bukti pengiriman untuk order %s terlampir.",
			"en": "The proof of delivery for order %s is attached.",
		},
	},
}

// renderDocumentEmail produces the subject and HTML body.
//
// Every interpolated value is HTML-escaped. These bodies carry order numbers
// and customer names that originate from user input, and an unescaped one would
// be a stored cross-site scripting vector in whatever renders the mail.
func renderDocumentEmail(templateName string, params *structpb.Struct, language string) (subject, body string) {
	lang := language
	if lang != "en" {
		lang = "id"
	}

	values := map[string]interface{}{}
	if params != nil {
		values = params.AsMap()
	}

	tmpl, ok := documentEmailTemplates[templateName]
	if !ok {
		// An unknown template still produces something deliverable rather than
		// an empty message.
		subject = "Karlo"
		body = wrapHTML("Karlo", renderParamList(values))
		return subject, body
	}

	args := orderedArgs(values)

	subject = safeFormat(pickLang(tmpl.Subject, lang), args)
	intro := safeFormat(pickLang(tmpl.Intro, lang), args)
	heading := pickLang(tmpl.Heading, lang)

	body = wrapHTML(heading, "<p>"+intro+"</p>"+renderParamList(values))
	return subject, body
}

// orderedArgs renders parameter values in a stable, escaped order.
func orderedArgs(values map[string]interface{}) []interface{} {
	// The document templates take at most two parameters, conventionally the
	// document number then the amount.
	keys := []string{"invoiceNumber", "orderNumber", "total", "amount", "number"}

	out := make([]interface{}, 0, 2)
	for _, k := range keys {
		if v, ok := values[k]; ok {
			out = append(out, html.EscapeString(fmt.Sprint(v)))
		}
	}
	return out
}

// safeFormat applies only as many arguments as the format string expects, so a
// mismatch produces short copy rather than "%!s(MISSING)".
func safeFormat(format string, args []interface{}) string {
	need := strings.Count(format, "%s")
	if need == 0 {
		return format
	}
	if len(args) < need {
		padded := make([]interface{}, need)
		copy(padded, args)
		for i := len(args); i < need; i++ {
			padded[i] = ""
		}
		args = padded
	}
	return fmt.Sprintf(format, args[:need]...)
}

func renderParamList(values map[string]interface{}) string {
	if len(values) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`<table style="border-collapse:collapse;margin-top:16px">`)
	for k, v := range values {
		fmt.Fprintf(&b,
			`<tr><td style="padding:4px 12px 4px 0;color:#666">%s</td><td style="padding:4px 0">%s</td></tr>`,
			html.EscapeString(k), html.EscapeString(fmt.Sprint(v)))
	}
	b.WriteString("</table>")
	return b.String()
}

func wrapHTML(heading, content string) string {
	return `<!doctype html><html><body style="font-family:system-ui,-apple-system,sans-serif;color:#111;line-height:1.5">` +
		`<h2 style="margin:0 0 12px">` + html.EscapeString(heading) + `</h2>` +
		content +
		`<p style="margin-top:24px;color:#888;font-size:12px">Karlo TMS</p>` +
		`</body></html>`
}

func pickLang(m map[string]string, lang string) string {
	if v, ok := m[lang]; ok {
		return v
	}
	return m["id"]
}
