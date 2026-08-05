// Package dpa generates a customer-specific Data Processing Agreement (DPA) as a
// branded PDF. Flomation Ltd is named as the Processor and the customer (an
// organisation or an individual account holder) as the Controller, following
// Article 28 of the UK GDPR and EU GDPR.
//
// The document is generated on demand and pre-executed on Flomation's side; the
// customer counter-signs. It is a solid, standards-aligned template and is
// marked as such - customers should have it reviewed by their own legal counsel
// before relying on it.
package dpa

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// Processor (Flomation) identity. Mirrors the invoice generator so the two
// documents present a consistent legal entity.
const (
	ProcessorName           = "Flomation Ltd"
	ProcessorCompanyNumber  = "16426271"
	ProcessorAddr1          = "Ruscoe House, The Chequer"
	ProcessorAddr2          = "Whitchurch, Wrexham"
	ProcessorAddr3          = "Wales, SY13 2JJ"
	ProcessorCountry        = "United Kingdom"
	ProcessorVAT            = "517 5918 67"
	ProcessorWebsite        = "www.flomation.co"
	ProcessorSignatory      = "Andrew Esser"
	ProcessorSignatoryTitle = "Director, for and on behalf of Flomation Ltd"
	ContactEmailSupport     = "privacy@flomation.co"
)

// TemplateVersion identifies the DPA template revision. Bump it whenever the
// clauses, annexes or layout change so a regenerated agreement is traceable to
// the template it was produced from. The value is stamped on every page footer
// and returned by the endpoint, and the DPA is always generated fresh on
// download (never cached), so template changes take effect immediately.
const TemplateVersion = "2026.08.1"

// Brand colours.
var (
	purple = []int{70, 0, 112}
	teal   = []int{0, 170, 156}
	ink    = []int{40, 40, 40}
	muted  = []int{120, 120, 120}
)

// Params identifies the Controller for a specific customer's agreement.
type Params struct {
	// ControllerType is "organisation" or "individual".
	ControllerType string
	// ControllerName is the display name (organisation name or person's name).
	ControllerName string
	// ControllerLegal is the registered legal-entity name where known; for an
	// individual this is their full name.
	ControllerLegal string
	// CompanyNumber is the registered company number (organisations only).
	CompanyNumber string
	// AddressLines is the registered/contact address, already assembled into
	// non-empty lines.
	AddressLines []string
	// ContactName and ContactEmail identify the person actioning the agreement.
	ContactName  string
	ContactEmail string
	// EffectiveDate is stamped on the agreement (normally the download date).
	EffectiveDate time.Time
	// Reference is a stable human-readable reference for the document.
	Reference string
}

// controllerLabel returns "organisation" or "individual" prose for the recitals.
func (p Params) controllerLabel() string {
	if p.ControllerType == "organisation" {
		return "organisation"
	}
	return "individual"
}

// legalName returns the best available legal identity for the Controller.
func (p Params) legalName() string {
	if strings.TrimSpace(p.ControllerLegal) != "" {
		return p.ControllerLegal
	}
	if strings.TrimSpace(p.ControllerName) != "" {
		return p.ControllerName
	}
	return "the Customer"
}

// doc wraps fpdf with clause numbering and the small set of typographic helpers
// the agreement needs.
type doc struct {
	pdf    *fpdf.Fpdf
	clause int
}

// GenerateFilename returns a filesystem-safe filename that matches the DPA's
// own reference (as shown on the document and in the Compliance tab), so the
// downloaded file is named the same as the agreement it contains.
func GenerateFilename(p Params) string {
	ref := sanitiseRef(p.Reference)
	if ref == "" {
		// Fallback when no reference is supplied: derive a stable name from the
		// controller so the download is still identifiable.
		slug := sanitiseSlug(p.ControllerName)
		if slug == "" {
			slug = "customer"
		}
		ref = "DPA-" + slug
	}
	return ref + ".pdf"
}

// sanitiseRef keeps a reference filesystem-safe (alphanumerics and hyphens),
// preserving its existing form (e.g. "DPA-60205F30").
func sanitiseRef(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

//go:embed logo.png
var logoPNG []byte

// GeneratePDF renders the agreement and returns the PDF bytes.
func GeneratePDF(p Params) ([]byte, error) {
	if p.EffectiveDate.IsZero() {
		p.EffectiveDate = time.Now()
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 20, 20)
	pdf.SetAutoPageBreak(true, 22)
	pdf.RegisterImageOptionsReader("logo", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logoPNG))

	d := &doc{pdf: pdf}
	d.registerFooter(p)

	pdf.AddPage()
	d.coverHeader(p)
	d.parties(p)
	d.background(p)
	d.definitions()
	d.clauses()
	d.annex1(p)
	d.annex2()
	d.signatures(p)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("generate DPA PDF: %w", err)
	}
	return buf.Bytes(), nil
}

// registerFooter draws the page number and reference at the foot of every page.
func (d *doc) registerFooter(p Params) {
	d.pdf.SetFooterFunc(func() {
		d.pdf.SetY(-15)
		d.pdf.SetFont("Helvetica", "", 7)
		setColor(d.pdf, muted)
		d.pdf.CellFormat(0, 5, fmt.Sprintf("%s  |  %s  |  Ref %s  |  Template v%s", ProcessorName, ProcessorWebsite, p.Reference, TemplateVersion), "", 0, "L", false, 0, "")
		d.pdf.CellFormat(0, 5, fmt.Sprintf("Page %d", d.pdf.PageNo()), "", 0, "R", false, 0, "")
	})
}

func (d *doc) coverHeader(p Params) {
	pdf := d.pdf
	pdf.ImageOptions("logo", 20, 18, 46, 0, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetFont("Helvetica", "", 8)
	setColor(pdf, muted)
	rightBlock := []string{ProcessorName, ProcessorAddr1, ProcessorAddr2, ProcessorAddr3, ProcessorCountry, "Company No. " + ProcessorCompanyNumber, "VAT " + ProcessorVAT}
	pdf.SetXY(110, 18)
	for _, line := range rightBlock {
		pdf.SetX(110)
		pdf.CellFormat(70, 4, line, "", 1, "R", false, 0, "")
	}

	pdf.SetY(48)
	pdf.SetFont("Helvetica", "B", 20)
	setColor(pdf, purple)
	pdf.CellFormat(0, 10, "Data Processing Agreement", "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	setColor(pdf, muted)
	pdf.CellFormat(0, 6, "Article 28 UK GDPR / EU GDPR - Controller to Processor", "", 1, "L", false, 0, "")
	pdf.Ln(2)

	// Reference / effective date strip.
	pdf.SetDrawColor(teal[0], teal[1], teal[2])
	pdf.SetLineWidth(0.6)
	pdf.Line(20, pdf.GetY(), 190, pdf.GetY())
	pdf.SetLineWidth(0.2)
	pdf.Ln(3)

	pdf.SetFont("Helvetica", "B", 9)
	setColor(pdf, ink)
	pdf.CellFormat(40, 6, "Reference", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 6, p.Reference, "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(40, 6, "Effective date", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 6, p.EffectiveDate.Format("02 January 2006"), "", 1, "L", false, 0, "")
	pdf.Ln(4)
}

func (d *doc) parties(p Params) {
	d.sectionTitle("The Parties")

	// Processor block.
	d.partyBlock("(1) The Processor", []string{
		ProcessorName,
		fmt.Sprintf("a company registered in England and Wales (company number %s, VAT %s)", ProcessorCompanyNumber, ProcessorVAT),
		ProcessorAddr1,
		ProcessorAddr2 + ", " + ProcessorAddr3,
		ProcessorCountry,
	})

	// Controller block.
	controllerLines := []string{d.legalHeadline(p)}
	if p.CompanyNumber != "" {
		controllerLines = append(controllerLines, "Company number: "+p.CompanyNumber)
	}
	controllerLines = append(controllerLines, p.AddressLines...)
	if p.ContactEmail != "" {
		controllerLines = append(controllerLines, "Contact: "+p.ContactEmail)
	}
	d.partyBlock("(2) The Controller", controllerLines)

	d.pdf.Ln(4)
	d.bodyText("This Agreement is made between the Processor and the Controller (each a \"party\" and together the \"parties\"). It governs the Processing of Personal Data by the Processor on behalf of the Controller in connection with the Controller's use of the Flomation workflow automation platform (the \"Services\"), and forms part of the agreement under which the Services are provided (the \"Principal Agreement\").")
}

// legalHeadline builds the Controller's headline identity line.
func (d *doc) legalHeadline(p Params) string {
	if p.ControllerType == "organisation" {
		return p.legalName()
	}
	// Individual controller.
	if strings.TrimSpace(p.ControllerName) != "" {
		return p.ControllerName + " (an individual account holder)"
	}
	return "The account holder"
}

func (d *doc) background(p Params) {
	d.sectionTitle("Background")
	d.bodyText(fmt.Sprintf("The Controller is an %s that uses the Services to design and run automated workflows. In doing so, the Controller may cause Personal Data for which it is the controller to be Processed by the Processor.", p.controllerLabel()))
	d.bodyText("The parties wish to set out the terms on which the Processor will Process that Personal Data, so as to comply with Article 28 of the UK GDPR and, where applicable, the EU GDPR, and to protect the rights of Data Subjects.")
	d.bodyText("The Controller at all times remains the owner and controller of the Personal Data it Processes through the Services. The Processor claims no ownership of, and acquires no rights in, that Personal Data, and Processes it only as set out in this Agreement.")
}

func (d *doc) definitions() {
	d.sectionTitle("1. Definitions")
	d.clause = 0
	defs := [][2]string{
		{"Data Protection Laws", "the UK GDPR, the Data Protection Act 2018, and, where applicable to the Processing, Regulation (EU) 2016/679 (the EU GDPR) and any other applicable law relating to the protection of Personal Data."},
		{"UK GDPR", "the retained EU law version of the General Data Protection Regulation (Regulation (EU) 2016/679) as it forms part of the law of England and Wales, Scotland and Northern Ireland."},
		{"Controller, Processor, Data Subject, Personal Data, Personal Data Breach, Processing and Supervisory Authority", "have the meanings given to them in the Data Protection Laws."},
		{"Sub-processor", "any third party engaged by the Processor to Process Personal Data on behalf of the Controller under this Agreement."},
		{"Services", "the Flomation workflow automation platform and related services provided by the Processor to the Controller."},
	}
	for _, def := range defs {
		d.definitionRow(def[0], def[1])
	}
}

func (d *doc) clauses() {
	d.numbered("Subject matter and duration", []string{
		"The subject matter, duration, nature and purpose of the Processing, the types of Personal Data and the categories of Data Subjects are set out in Annex 1.",
		"This Agreement takes effect on the Effective date and continues for as long as the Processor Processes Personal Data on behalf of the Controller under the Principal Agreement.",
	})

	d.numbered("Processing only on documented instructions", []string{
		"The Processor shall Process the Personal Data only on the documented instructions of the Controller, including with regard to transfers of Personal Data to a third country, unless required to do otherwise by law; in which case the Processor shall inform the Controller of that legal requirement before Processing, unless the law prohibits such information on important grounds of public interest.",
		"The Controller's instructions are set out in this Agreement, the Principal Agreement, and the configuration of the workflows the Controller creates within the Services. The Processor shall immediately inform the Controller if, in its opinion, an instruction infringes the Data Protection Laws.",
	})

	d.numbered("Confidentiality", []string{
		"The Processor shall ensure that persons authorised to Process the Personal Data have committed themselves to confidentiality or are under an appropriate statutory obligation of confidentiality, and shall limit access to those who need it to provide the Services.",
	})

	d.numbered("Security of Processing", []string{
		"Taking into account the state of the art, the costs of implementation and the nature, scope, context and purposes of Processing, as well as the risk to Data Subjects, the Processor shall implement appropriate technical and organisational measures to ensure a level of security appropriate to the risk, as further described in Annex 2.",
		"Those measures shall include, as appropriate, the pseudonymisation and encryption of Personal Data; the ability to ensure the ongoing confidentiality, integrity, availability and resilience of Processing systems; the ability to restore availability and access in a timely manner after an incident; and a process for regularly testing and evaluating the effectiveness of those measures.",
	})

	d.numbered("Sub-processors", []string{
		"The Controller grants the Processor general authorisation to engage Sub-processors to Process the Personal Data, subject to this clause. The Processor shall inform the Controller of any intended addition or replacement of a Sub-processor, giving the Controller the opportunity to object on reasonable data-protection grounds.",
		"The Processor shall impose on each Sub-processor, by written contract, data-protection obligations no less protective than those set out in this Agreement, and shall remain fully liable to the Controller for the performance of each Sub-processor's obligations.",
	})

	d.numbered("International transfers", []string{
		"The Processor shall not transfer the Personal Data to a country outside the United Kingdom or, where the EU GDPR applies, the European Economic Area, unless it has taken such measures as are necessary to ensure the transfer is lawful under the Data Protection Laws, which may include the use of an adequacy decision, the International Data Transfer Agreement, the UK Addendum to the EU Standard Contractual Clauses, or another lawful transfer mechanism.",
	})

	d.numbered("Assistance to the Controller", []string{
		"Taking into account the nature of the Processing, the Processor shall assist the Controller by appropriate technical and organisational measures, insofar as this is possible, in fulfilling the Controller's obligation to respond to requests from Data Subjects exercising their rights under the Data Protection Laws.",
		"The Processor shall assist the Controller in ensuring compliance with its obligations relating to security of Processing, notification of Personal Data Breaches, data protection impact assessments and prior consultation with the Supervisory Authority, taking into account the nature of Processing and the information available to the Processor.",
	})

	d.numbered("Personal Data Breach", []string{
		"The Processor shall notify the Controller without undue delay after becoming aware of a Personal Data Breach affecting the Controller's Personal Data, and shall provide the Controller with sufficient information to allow it to meet any obligation to report the breach to a Supervisory Authority or to inform affected Data Subjects.",
		fmt.Sprintf("Breach notifications and data-protection enquiries may be sent to the Processor at %s.", ContactEmailSupport),
	})

	d.numbered("Ownership of and rights over the data", []string{
		"As between the parties, all Personal Data and other data that the Controller Processes through the Services remain the property of the Controller. The Processor acquires no right, title or interest in that data other than the limited right to Process it to provide the Services.",
		"The Controller may access, export and request the deletion of its Personal Data at any time through the Services or by written request to the Processor. The Processor shall give effect to a documented deletion request without undue delay, subject only to any Processing the Processor is required by law to continue.",
	})

	d.numbered("Return and deletion on termination", []string{
		"On termination of the Services, and at the choice of the Controller, the Processor shall delete or return all the Personal Data to the Controller and delete existing copies, unless the law requires storage of the Personal Data.",
		"Where the Controller does not express a choice, the Processor shall delete the Personal Data within a reasonable period following termination, save for copies retained in routine backups which are overwritten in the ordinary course and which the Processor shall not access other than as required by law.",
	})

	d.numbered("Audit and information", []string{
		"The Processor shall make available to the Controller all information necessary to demonstrate compliance with the obligations in Article 28 of the UK GDPR and this Agreement, and shall allow for and contribute to audits, including inspections, conducted by the Controller or an auditor mandated by the Controller, on reasonable prior notice and subject to appropriate confidentiality undertakings.",
	})

	d.numbered("Controller obligations", []string{
		"The Controller warrants that it has a lawful basis for the Processing it instructs, that it has provided any notices and obtained any consents required under the Data Protection Laws, and that its instructions will not cause the Processor to breach the Data Protection Laws.",
	})

	d.numbered("Liability and indemnity", []string{
		"Each party's liability arising out of or in connection with this Agreement is subject to the limitations and exclusions of liability set out in the Principal Agreement. Nothing in this Agreement limits either party's liability to the extent it cannot be limited under the Data Protection Laws.",
	})

	d.numbered("Governing law and jurisdiction", []string{
		"This Agreement is governed by the laws of England and Wales, and the parties submit to the exclusive jurisdiction of the courts of England and Wales, without prejudice to any mandatory rights a Data Subject may have under the Data Protection Laws of their place of residence.",
	})

	d.numbered("Precedence", []string{
		"In the event of any conflict between this Agreement and the Principal Agreement in respect of the Processing of Personal Data, this Agreement prevails.",
	})
}

func (d *doc) annex1(p Params) {
	d.pdf.AddPage()
	d.sectionTitle("Annex 1 - Details of the Processing")

	d.annexRow("Controller", d.legalHeadline(p))
	d.annexRow("Processor", ProcessorName)
	d.annexRow("Subject matter", "Provision of the Flomation workflow automation platform to the Controller.")
	d.annexRow("Duration", "For the term of the Principal Agreement and any period during which the Processor Processes Personal Data on behalf of the Controller.")
	d.annexRow("Nature and purpose", "Hosting, storage, execution and orchestration of the Controller's automated workflows, including running the actions and integrations the Controller configures, so as to deliver the Services.")
	d.annexRow("Types of Personal Data", "As determined by the Controller through its configuration of the Services. This may include names, contact details, account identifiers, message and conversation content, form submissions, and any other Personal Data the Controller chooses to Process through its workflows.")
	d.annexRow("Categories of Data Subjects", "As determined by the Controller. This may include the Controller's staff, customers, prospects, suppliers, correspondents and other individuals whose data the Controller Processes through the Services.")
	d.annexRow("Sub-processors", "Cloud hosting and infrastructure providers, and any integration or service provider the Controller expressly configures a workflow to use. A current list is available from the Processor on request.")
}

func (d *doc) annex2() {
	d.pdf.AddPage()
	d.sectionTitle("Annex 2 - Technical and Organisational Measures")
	d.bodyText("The Processor implements and maintains appropriate technical and organisational measures, which include the following:")
	measures := []string{
		"Encryption of Personal Data in transit using current TLS, and encryption of sensitive data (including credentials and secrets) at rest.",
		"Role-based access control, with access to Personal Data limited to authorised personnel on a need-to-know basis.",
		"Segregation of customer environments and data within the platform.",
		"Authentication controls including support for multi-factor authentication, and secure session management.",
		"Logging and monitoring of access to and Processing of Personal Data, and alerting on anomalous activity.",
		"Regular patching of systems and dependencies, and vulnerability scanning of the platform.",
		"Backups with defined retention, and tested procedures for restoring availability and access following an incident.",
		"Secure software development practices, including code review and automated security checks before release.",
		"A documented Personal Data Breach response process, including notification of the Controller without undue delay.",
		"Staff confidentiality obligations and data-protection awareness.",
	}
	for _, m := range measures {
		d.bullet(m)
	}
	d.pdf.Ln(2)
	d.disclaimer()
}

func (d *doc) signatures(p Params) {
	pdf := d.pdf
	pdf.Ln(6)
	d.sectionTitle("Signatures")
	d.bodyText("Signed for and on behalf of the parties by their duly authorised representatives.")
	pdf.Ln(2)

	colW := 82.0
	gap := 6.0
	top := pdf.GetY()

	// Processor - pre-executed.
	d.signatureBox(20, top, colW, "The Processor", ProcessorName, ProcessorSignatory, ProcessorSignatoryTitle, p.EffectiveDate.Format("02 January 2006"), true)
	// Controller - to sign.
	d.signatureBox(20+colW+gap, top, colW, "The Controller", p.legalName(), p.ContactName, controllerSignTitle(p), "", false)
}

func controllerSignTitle(p Params) string {
	if p.ControllerType == "organisation" {
		return "Authorised signatory"
	}
	return "Account holder"
}

// ── low-level rendering helpers ───────────────────────────────────────────

func (d *doc) sectionTitle(title string) {
	pdf := d.pdf
	pdf.Ln(7)
	// Keep the section title, its rule and the first line of content together.
	d.ensureSpace(30)
	pdf.SetFont("Helvetica", "B", 12.5)
	setColor(pdf, purple)
	pdf.MultiCell(0, 7, title, "", "L", false)
	pdf.Ln(1.5)
	pdf.SetDrawColor(220, 220, 220)
	pdf.Line(20, pdf.GetY(), 190, pdf.GetY())
	pdf.Ln(5)
}

func (d *doc) bodyText(text string) {
	pdf := d.pdf
	pdf.SetFont("Helvetica", "", 9.5)
	setColor(pdf, ink)
	pdf.MultiCell(0, 5, latin1(text), "", "J", false)
	pdf.Ln(2.5)
}

// ensureSpace forces a page break when fewer than h mm remain before the
// bottom margin, so a heading is never orphaned at the foot of a page.
func (d *doc) ensureSpace(h float64) {
	_, pageH := d.pdf.GetPageSize()
	_, _, _, bottom := d.pdf.GetMargins()
	if d.pdf.GetY()+h > pageH-bottom {
		d.pdf.AddPage()
	}
}

// hangingParagraph renders a labelled paragraph with a hanging indent that
// survives page breaks: the label sits in a fixed-width left column and the
// body wraps against a temporarily-widened left margin. It deliberately does
// NOT capture a Y coordinate — a mid-paragraph auto page break would make a
// captured Y point at the previous page, which corrupted the layout before.
func (d *doc) hangingParagraph(label, text string, indent float64) {
	pdf := d.pdf
	x := pdf.GetX()
	pdf.CellFormat(indent, 5, latin1(label), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9.5)
	pdf.SetLeftMargin(x + indent)
	pdf.MultiCell(0, 5, latin1(text), "", "J", false)
	pdf.SetLeftMargin(x)
	pdf.SetX(x)
}

// numbered renders a top-level clause with an auto-incrementing number and its
// sub-paragraphs numbered N.1, N.2, ...
func (d *doc) numbered(heading string, paras []string) {
	d.clause++
	pdf := d.pdf
	pdf.Ln(3)
	// Keep the clause heading with the start of its first paragraph so a
	// heading never lands alone at the foot of a page.
	d.ensureSpace(26)
	pdf.SetFont("Helvetica", "B", 10.5)
	setColor(pdf, ink)
	pdf.MultiCell(0, 6, latin1(fmt.Sprintf("%d. %s", d.clause+1, heading)), "", "L", false)
	pdf.Ln(1)
	for i, para := range paras {
		setColor(pdf, ink)
		pdf.SetFont("Helvetica", "B", 9.5)
		d.hangingParagraph(fmt.Sprintf("%d.%d", d.clause+1, i+1), para, 12)
		pdf.Ln(1.5)
	}
}

func (d *doc) definitionRow(term, meaning string) {
	pdf := d.pdf
	pdf.SetFont("Helvetica", "B", 9.5)
	setColor(pdf, ink)
	pdf.MultiCell(0, 5, latin1("\""+term+"\""), "", "L", false)
	pdf.SetFont("Helvetica", "", 9.5)
	pdf.MultiCell(0, 5, latin1("means "+meaning), "", "J", false)
	pdf.Ln(2.5)
}

func (d *doc) partyBlock(label string, lines []string) {
	pdf := d.pdf
	pdf.Ln(3.5)
	pdf.SetFont("Helvetica", "B", 10)
	setColor(pdf, teal)
	pdf.CellFormat(0, 5, label, "", 1, "L", false, 0, "")
	pdf.Ln(1)
	setColor(pdf, ink)
	for i, line := range lines {
		if i == 0 {
			pdf.SetFont("Helvetica", "B", 9.5)
		} else {
			pdf.SetFont("Helvetica", "", 9)
		}
		pdf.MultiCell(0, 4.8, latin1(line), "", "L", false)
	}
}

func (d *doc) annexRow(label, value string) {
	pdf := d.pdf
	pdf.SetFont("Helvetica", "B", 9.5)
	setColor(pdf, teal)
	pdf.MultiCell(0, 5, label, "", "L", false)
	pdf.Ln(0.5)
	pdf.SetFont("Helvetica", "", 9.5)
	setColor(pdf, ink)
	pdf.MultiCell(0, 5, latin1(value), "", "J", false)
	pdf.Ln(3)
}

func (d *doc) bullet(text string) {
	pdf := d.pdf
	pdf.SetFont("Helvetica", "", 9.5)
	setColor(pdf, ink)
	d.hangingParagraph("-", text, 5)
	pdf.Ln(0.5)
}

func (d *doc) disclaimer() {
	pdf := d.pdf
	pdf.Ln(2)
	pdf.SetFillColor(245, 243, 250)
	pdf.SetFont("Helvetica", "I", 8)
	setColor(pdf, muted)
	pdf.MultiCell(0, 4.5, latin1("This Data Processing Agreement is provided by Flomation Ltd as a standards-aligned template. It does not constitute legal advice. The Controller should review it, and have it reviewed by its own legal advisers, to confirm it meets its specific requirements before relying on it."), "1", "L", true)
}

func (d *doc) signatureBox(x, y, w float64, role, entity, name, title, dateStr string, preSigned bool) {
	pdf := d.pdf
	pdf.SetXY(x, y)
	pdf.SetFont("Helvetica", "B", 9)
	setColor(pdf, teal)
	pdf.CellFormat(w, 5, role, "", 2, "L", false, 0, "")
	pdf.SetX(x)
	pdf.SetFont("Helvetica", "B", 9.5)
	setColor(pdf, ink)
	pdf.CellFormat(w, 5, latin1(entity), "", 2, "L", false, 0, "")
	pdf.Ln(4)

	// Signature line.
	pdf.SetX(x)
	if preSigned {
		pdf.SetFont("Times", "I", 15)
		setColor(pdf, purple)
		pdf.CellFormat(w, 8, latin1(name), "", 2, "L", false, 0, "")
	} else {
		pdf.Ln(8)
	}
	pdf.SetX(x)
	pdf.SetDrawColor(160, 160, 160)
	lineY := pdf.GetY()
	pdf.Line(x, lineY, x+w, lineY)
	pdf.Ln(1)

	pdf.SetX(x)
	pdf.SetFont("Helvetica", "", 8)
	setColor(pdf, muted)
	pdf.CellFormat(w, 4.5, "Signature", "", 2, "L", false, 0, "")

	pdf.Ln(2)
	d.sigField(x, w, "Name", name)
	d.sigField(x, w, "Title", title)
	d.sigField(x, w, "Date", dateStr)
}

func (d *doc) sigField(x, w float64, label, value string) {
	pdf := d.pdf
	pdf.SetX(x)
	pdf.SetFont("Helvetica", "B", 8)
	setColor(pdf, ink)
	pdf.CellFormat(14, 5, label, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 8)
	setColor(pdf, muted)
	shown := value
	if strings.TrimSpace(shown) == "" {
		shown = strings.Repeat(".", 40)
	}
	pdf.CellFormat(w-14, 5, latin1(shown), "", 2, "L", false, 0, "")
}

func setColor(pdf *fpdf.Fpdf, c []int) {
	pdf.SetTextColor(c[0], c[1], c[2])
}

// latin1 downgrades common UTF-8 punctuation to ISO-8859-1 so fpdf (which uses
// the Latin-1 code page) renders it correctly rather than dropping characters.
func latin1(s string) string {
	r := strings.NewReplacer(
		"’", "'", // right single quote
		"‘", "'", // left single quote
		"“", "\"", // left double quote
		"”", "\"", // right double quote
		"–", "-", // en dash
		"—", "-", // em dash
		"…", "...", // ellipsis
		" ", " ", // non-breaking space
		"£", "\xa3", // pound sign
	)
	return r.Replace(s)
}

func sanitiseSlug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
