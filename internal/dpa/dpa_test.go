package dpa

import (
	"bytes"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

func TestGeneratePDF_ProducesValidPDF(t *testing.T) {
	RegisterTestingT(t)

	p := Params{
		ControllerType:  "organisation",
		ControllerName:  "Acme Widgets",
		ControllerLegal: "Acme Widgets Limited",
		CompanyNumber:   "12345678",
		AddressLines:    []string{"1 High Street", "Manchester", "M1 1AA", "United Kingdom"},
		ContactName:     "Andy Esser",
		ContactEmail:    "andy@example.com",
		EffectiveDate:   time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		Reference:       "DPA-0A1B2C3D",
	}

	out, err := GeneratePDF(p)
	Expect(err).ToNot(HaveOccurred())
	// A real PDF starts with the %PDF- magic and is more than a trivial stub.
	Expect(bytes.HasPrefix(out, []byte("%PDF-"))).To(BeTrue())
	Expect(len(out)).To(BeNumerically(">", 3000))
}

func TestGeneratePDF_IndividualAndDefaultsDate(t *testing.T) {
	RegisterTestingT(t)

	// Zero EffectiveDate must default to "now" rather than render 0001-01-01.
	p := Params{
		ControllerType:  "individual",
		ControllerName:  "Jane Doe",
		ControllerLegal: "Jane Doe",
		ContactName:     "Jane Doe",
		Reference:       "DPA-11112222",
	}
	out, err := GeneratePDF(p)
	Expect(err).ToNot(HaveOccurred())
	Expect(bytes.HasPrefix(out, []byte("%PDF-"))).To(BeTrue())
}

func TestGenerateFilename(t *testing.T) {
	RegisterTestingT(t)

	p := Params{
		ControllerName: "Acme Widgets Ltd!",
		EffectiveDate:  time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	}
	Expect(GenerateFilename(p)).To(Equal("flomation-dpa-acme-widgets-ltd-20260805.pdf"))

	// Empty name → stable fallback slug.
	Expect(GenerateFilename(Params{EffectiveDate: p.EffectiveDate})).To(Equal("flomation-dpa-customer-20260805.pdf"))
}

func TestLatin1_DowngradesPunctuationAndDropsNoEmDash(t *testing.T) {
	RegisterTestingT(t)

	// Smart quotes and dashes must be downgraded so fpdf renders them.
	got := latin1("The Controller’s “data” – owned — always")
	Expect(got).To(Equal("The Controller's \"data\" - owned - always"))
}

func TestSanitiseSlug(t *testing.T) {
	RegisterTestingT(t)

	Expect(sanitiseSlug("Acme Widgets Ltd!")).To(Equal("acme-widgets-ltd"))
	Expect(sanitiseSlug("  --Beta & Co--  ")).To(Equal("beta-co"))
	Expect(sanitiseSlug("!!!")).To(Equal(""))
}
