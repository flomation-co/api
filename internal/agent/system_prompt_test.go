package agent

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func TestBuildSystemPrompt_HonestyDirectiveAlwaysPresent(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("Layer 0"))
	Expect(prompt).To(ContainSubstring("time-bounded commitments"))
}

func TestBuildSystemPrompt_PersonaFirst(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("You are Ada.", nil, nil, nil, nil, nil, "")
	lines := strings.Split(prompt, "\n")
	Expect(lines[0]).To(Equal("You are Ada."))
}

func TestBuildSystemPrompt_NoPersona(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "")
	Expect(prompt).NotTo(HavePrefix("\n"))
	Expect(prompt).To(HavePrefix("━━━"))
}

func TestBuildSystemPrompt_CurrentTime(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("Current time"))
}

func TestBuildSystemPrompt_PinnedMemories(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Location", Body: "Lives in Leeds", Type: "fact"},
		{Title: "Preference", Body: "Prefers dark mode", Type: "preference"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("What you know about this user"))
	Expect(prompt).To(ContainSubstring("• Location: Lives in Leeds"))
	Expect(prompt).To(ContainSubstring("• Preference: Prefers dark mode"))
}

func TestBuildSystemPrompt_EmptyTitleUsesBodyOnly(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "", Body: "Some fact about the user", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("• Some fact about the user"))
	Expect(prompt).NotTo(ContainSubstring(": Some fact"))
}

func TestBuildSystemPrompt_DuplicateTitleBody(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Same text", Body: "Same text", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("• Same text\n"))
	Expect(prompt).NotTo(ContainSubstring("Same text: Same text"))
}

func TestBuildSystemPrompt_NoMemoriesOmitsSection(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("Persona", nil, nil, nil, nil, nil, "")
	Expect(prompt).NotTo(ContainSubstring("What you know"))
}

func TestBuildSystemPrompt_RelevantMemories(t *testing.T) {
	RegisterTestingT(t)

	relevant := []assembledMemory{
		{Title: "Previous chat", Body: "Discussed holiday plans", Type: "session_summary"},
	}

	prompt := BuildSystemPrompt("", nil, relevant, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("Relevant context"))
	Expect(prompt).To(ContainSubstring("Previous chat: Discussed holiday plans"))
}

func TestBuildSystemPrompt_ActiveTasks(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Book plumber", Body: "Fix kitchen tap", Type: "task"},
		{Title: "Location", Body: "Leeds", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "")
	Expect(prompt).To(ContainSubstring("Active tasks"))
	Expect(prompt).To(ContainSubstring("Book plumber"))
	// Facts appear in "What you know" but NOT in "Active tasks"
	activeIdx := strings.Index(prompt, "Active tasks")
	activeSection := prompt[activeIdx:]
	Expect(activeSection).NotTo(ContainSubstring("Location"))
}

func TestBuildSystemPrompt_ActiveTasksDedup(t *testing.T) {
	RegisterTestingT(t)

	pinned := []assembledMemory{
		{Title: "Book plumber", Body: "Fix tap", Type: "task"},
	}
	relevant := []assembledMemory{
		{Title: "Book plumber", Body: "Fix tap", Type: "task"},
	}

	prompt := BuildSystemPrompt("", pinned, relevant, nil, nil, nil, "")
	Expect(strings.Count(prompt, "Book plumber")).To(BeNumerically("<=", 3))
}

func TestBuildSystemPrompt_SlackDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "slack")
	Expect(prompt).To(ContainSubstring("mrkdwn"))
	Expect(prompt).To(ContainSubstring("Current channel"))
}

func TestBuildSystemPrompt_TelegramDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "telegram")
	Expect(prompt).To(ContainSubstring("Telegram"))
	Expect(prompt).To(ContainSubstring("4096"))
}

func TestBuildSystemPrompt_EmailDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "email")
	Expect(prompt).To(ContainSubstring("plain text"))
	Expect(prompt).To(ContainSubstring("tool calls"))
}

func TestBuildSystemPrompt_UnknownChannelNoDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "unknown")
	Expect(prompt).NotTo(ContainSubstring("Current channel"))
}

func TestBuildSystemPrompt_PendingActions(t *testing.T) {
	RegisterTestingT(t)

	pending := []assembledPendingAction{
		{Type: "identity_link", Evidence: "I'm also andy@email.com"},
	}

	prompt := BuildSystemPrompt("", nil, nil, pending, nil, nil, "")
	Expect(prompt).To(ContainSubstring("PENDING ITEMS"))
	Expect(prompt).To(ContainSubstring("Identity link"))
	Expect(prompt).To(ContainSubstring("andy@email.com"))
}

func TestBuildSystemPrompt_PendingVerification(t *testing.T) {
	RegisterTestingT(t)

	pending := []assembledPendingAction{
		{Type: "identity_link_verification", Evidence: "Claims to be from Telegram"},
	}

	prompt := BuildSystemPrompt("", nil, nil, pending, nil, nil, "")
	Expect(prompt).To(ContainSubstring("Identity verification"))
}

func TestBuildSystemPrompt_ToolsSection(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/email_send", Name: "Email Send", Description: "Send an email"},
		{Type: "tools/calendar_read", Name: "Calendar Read", Description: "Read calendar"},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "")
	Expect(prompt).To(ContainSubstring("━━━ Tools ━━━"))
	Expect(prompt).To(ContainSubstring("Email Send"))
	Expect(prompt).To(ContainSubstring("Calendar Read"))
}

func TestBuildSystemPrompt_TelegramDirectivePresent(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/channel_action", Name: "Channel Action", Description: "Channel actions"},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "telegram")
	Expect(prompt).To(ContainSubstring("Telegram"))
	Expect(prompt).To(ContainSubstring("Channel Action"))
}

func TestBuildSystemPrompt_SlackHasTools(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/channel_action", Name: "Channel Action", Description: ""},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "slack")
	Expect(prompt).To(ContainSubstring("Channel Action"))
}

func TestBuildSystemPrompt_SectionOrdering(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{{Title: "T", Body: "B", Type: "fact"}}
	pending := []assembledPendingAction{{Type: "test", Evidence: "ev"}}
	tools := []AssembledTool{{Type: "t", Name: "N", Description: "D"}}

	prompt := BuildSystemPrompt("Persona", mems, nil, pending, nil, tools, "slack")

	personaIdx := strings.Index(prompt, "Persona")
	timeIdx := strings.Index(prompt, "Current time")
	layerIdx := strings.Index(prompt, "Layer 0")
	toolsIdx := strings.Index(prompt, "━━━ Tools")
	memIdx := strings.Index(prompt, "What you know")
	channelIdx := strings.Index(prompt, "Current channel")
	actionIdx := strings.Index(prompt, "PENDING ITEMS")

	Expect(personaIdx).To(BeNumerically("<", timeIdx))
	Expect(timeIdx).To(BeNumerically("<", layerIdx))
	Expect(layerIdx).To(BeNumerically("<", toolsIdx))
	Expect(toolsIdx).To(BeNumerically("<", memIdx))
	Expect(memIdx).To(BeNumerically("<", channelIdx))
	Expect(channelIdx).To(BeNumerically("<", actionIdx))
}

func TestBuildSystemPrompt_TrailingNewline(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("Test", nil, nil, nil, nil, nil, "")
	Expect(prompt).To(HaveSuffix("\n"))
	Expect(prompt).NotTo(HaveSuffix("\n\n"))
}

func TestChannelDirective_AllTypes(t *testing.T) {
	RegisterTestingT(t)

	Expect(ChannelDirective("slack")).To(ContainSubstring("mrkdwn"))
	Expect(ChannelDirective("telegram")).To(ContainSubstring("4096"))
	Expect(ChannelDirective("email")).To(ContainSubstring("plain text"))
	Expect(ChannelDirective("webhook")).To(ContainSubstring("concisely"))
	Expect(ChannelDirective("unknown")).To(Equal(""))
	Expect(ChannelDirective("")).To(Equal(""))
}
