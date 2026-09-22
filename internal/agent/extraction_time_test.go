package agent

import (
	"testing"
	"time"

	"github.com/onsi/gomega"
)

func TestExtractionCurrentTimeNamesTheDayAndTheYear(t *testing.T) {
	gomega.RegisterTestingT(t)

	rendered := ExtractionCurrentTime(time.Date(2026, time.September, 10, 9, 5, 0, 0, time.UTC))

	gomega.Expect(rendered).To(gomega.ContainSubstring("Thursday"),
		"assistants phrase promises by weekday, so the weekday has to be given rather than computed")
	gomega.Expect(rendered).To(gomega.ContainSubstring("10 September 2026"))
	gomega.Expect(rendered).To(gomega.ContainSubstring("2026-09-10T09:05:00Z"))
}
