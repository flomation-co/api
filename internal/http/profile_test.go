package http

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestComposeFullName(t *testing.T) {
	RegisterTestingT(t)

	// All three parts present
	Expect(composeFullName("Mr", "Andy", "Esser", "andy@x.com")).To(Equal("Mr Andy Esser"))

	// First + last only
	Expect(composeFullName("", "Andy", "Esser", "andy@x.com")).To(Equal("Andy Esser"))

	// Salutation + first only
	Expect(composeFullName("Dr", "Andy", "", "andy@x.com")).To(Equal("Dr Andy"))

	// Whitespace fields elided
	Expect(composeFullName("  ", "Andy", "  ", "fallback")).To(Equal("Andy"))

	// Empty → falls back to display name
	Expect(composeFullName("", "", "", "Andy Esser")).To(Equal("Andy Esser"))

	// Empty display name on empty input → ""
	Expect(composeFullName("", "", "", "")).To(Equal(""))
}

func TestComposeFullAddress(t *testing.T) {
	RegisterTestingT(t)

	// Full UK-style address
	got := composeFullAddress("Ruscoe House", "The Chequer", "Whitchurch", "Wrexham", "SY13 2JJ", "Wales")
	Expect(got).To(Equal("Ruscoe House\nThe Chequer\nWhitchurch\nWrexham\nSY13 2JJ\nWales"))

	// Missing line_2 collapses
	got = composeFullAddress("10 Downing St", "", "London", "", "SW1A 2AA", "United Kingdom")
	Expect(got).To(Equal("10 Downing St\nLondon\nSW1A 2AA\nUnited Kingdom"))

	// All empty → empty string
	Expect(composeFullAddress("", "", "", "", "", "")).To(Equal(""))

	// Whitespace-only fields elided
	Expect(composeFullAddress("Line One", "   ", "City", "", "", "")).To(Equal("Line One\nCity"))
}

func TestTrimOrNil(t *testing.T) {
	RegisterTestingT(t)

	Expect(trimOrNil(nil)).To(BeNil())

	empty := ""
	Expect(trimOrNil(&empty)).To(BeNil())

	whitespace := "   \t  "
	Expect(trimOrNil(&whitespace)).To(BeNil())

	value := "  Andy  "
	got := trimOrNil(&value)
	Expect(got).ToNot(BeNil())
	Expect(*got).To(Equal("Andy"))
}
