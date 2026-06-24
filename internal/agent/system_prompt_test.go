package agent

import (
	"strings"
	"testing"
	"time"

	apipersistence "flomation.app/automate/api/internal/persistence"
	. "github.com/onsi/gomega"
)

func TestBuildSystemPrompt_HonestyDirectiveAlwaysPresent(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("Layer 0"))
	Expect(prompt).To(ContainSubstring("time-bounded commitments"))
}

func TestBuildSystemPrompt_PersonaFirst(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("You are Ada.", nil, nil, nil, nil, nil, "", nil)
	lines := strings.Split(prompt, "\n")
	Expect(lines[0]).To(Equal("You are Ada."))
}

func TestBuildSystemPrompt_NoPersona(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "", nil)
	Expect(prompt).NotTo(HavePrefix("\n"))
	Expect(prompt).To(HavePrefix("━━━"))
}

func TestBuildSystemPrompt_CurrentTime(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("Current time"))
}

func TestBuildSystemPrompt_PinnedMemories(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Location", Body: "Lives in Leeds", Type: "fact"},
		{Title: "Preference", Body: "Prefers dark mode", Type: "preference"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("What you know about this user"))
	Expect(prompt).To(ContainSubstring("• Location: Lives in Leeds"))
	Expect(prompt).To(ContainSubstring("• Preference: Prefers dark mode"))
}

func TestBuildSystemPrompt_EmptyTitleUsesBodyOnly(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "", Body: "Some fact about the user", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("• Some fact about the user"))
	Expect(prompt).NotTo(ContainSubstring(": Some fact"))
}

func TestBuildSystemPrompt_DuplicateTitleBody(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Same text", Body: "Same text", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("• Same text\n"))
	Expect(prompt).NotTo(ContainSubstring("Same text: Same text"))
}

func TestBuildSystemPrompt_NoMemoriesOmitsSection(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("Persona", nil, nil, nil, nil, nil, "", nil)
	Expect(prompt).NotTo(ContainSubstring("What you know"))
}

func TestBuildSystemPrompt_RelevantMemories(t *testing.T) {
	RegisterTestingT(t)

	relevant := []assembledMemory{
		{Title: "Previous chat", Body: "Discussed holiday plans", Type: "session_summary"},
	}

	prompt := BuildSystemPrompt("", nil, relevant, nil, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("Relevant context"))
	Expect(prompt).To(ContainSubstring("Previous chat: Discussed holiday plans"))
}

func TestBuildSystemPrompt_ActiveTasks(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{
		{Title: "Book plumber", Body: "Fix kitchen tap", Type: "task"},
		{Title: "Location", Body: "Leeds", Type: "fact"},
	}

	prompt := BuildSystemPrompt("", mems, nil, nil, nil, nil, "", nil)
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

	prompt := BuildSystemPrompt("", pinned, relevant, nil, nil, nil, "", nil)
	Expect(strings.Count(prompt, "Book plumber")).To(BeNumerically("<=", 3))
}

func TestBuildSystemPrompt_SlackDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "slack", nil)
	Expect(prompt).To(ContainSubstring("mrkdwn"))
	Expect(prompt).To(ContainSubstring("Current channel"))
}

func TestBuildSystemPrompt_TelegramDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "telegram", nil)
	Expect(prompt).To(ContainSubstring("Telegram"))
	Expect(prompt).To(ContainSubstring("4096"))
}

func TestBuildSystemPrompt_EmailDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "email", nil)
	Expect(prompt).To(ContainSubstring("plain text"))
	Expect(prompt).To(ContainSubstring("tool calls"))
}

func TestBuildSystemPrompt_UnknownChannelNoDirective(t *testing.T) {
	RegisterTestingT(t)

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, nil, "unknown", nil)
	Expect(prompt).NotTo(ContainSubstring("Current channel"))
}

func TestBuildSystemPrompt_PendingActions(t *testing.T) {
	RegisterTestingT(t)

	pending := []assembledPendingAction{
		{Type: "identity_link", Evidence: "I'm also andy@email.com"},
	}

	prompt := BuildSystemPrompt("", nil, nil, pending, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("PENDING ITEMS"))
	Expect(prompt).To(ContainSubstring("Identity link"))
	Expect(prompt).To(ContainSubstring("andy@email.com"))
}

func TestBuildSystemPrompt_PendingVerification(t *testing.T) {
	RegisterTestingT(t)

	pending := []assembledPendingAction{
		{Type: "identity_link_verification", Evidence: "Claims to be from Telegram"},
	}

	prompt := BuildSystemPrompt("", nil, nil, pending, nil, nil, "", nil)
	Expect(prompt).To(ContainSubstring("Identity verification"))
}

func TestBuildSystemPrompt_ToolsSection(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/email_send", Name: "Email Send", Description: "Send an email"},
		{Type: "tools/calendar_read", Name: "Calendar Read", Description: "Read calendar"},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "", nil)
	Expect(prompt).To(ContainSubstring("━━━ Tools ━━━"))
	Expect(prompt).To(ContainSubstring("Email Send"))
	Expect(prompt).To(ContainSubstring("Calendar Read"))
}

func TestBuildSystemPrompt_TelegramDirectivePresent(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/channel_action", Name: "Channel Action", Description: "Channel actions"},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "telegram", nil)
	Expect(prompt).To(ContainSubstring("Telegram"))
	Expect(prompt).To(ContainSubstring("Channel Action"))
}

func TestBuildSystemPrompt_SlackHasTools(t *testing.T) {
	RegisterTestingT(t)

	tools := []AssembledTool{
		{Type: "tools/channel_action", Name: "Channel Action", Description: ""},
	}

	prompt := BuildSystemPrompt("", nil, nil, nil, nil, tools, "slack", nil)
	Expect(prompt).To(ContainSubstring("Channel Action"))
}

func TestBuildSystemPrompt_SectionOrdering(t *testing.T) {
	RegisterTestingT(t)

	mems := []assembledMemory{{Title: "T", Body: "B", Type: "fact"}}
	pending := []assembledPendingAction{{Type: "test", Evidence: "ev"}}
	tools := []AssembledTool{{Type: "t", Name: "N", Description: "D"}}

	prompt := BuildSystemPrompt("Persona", mems, nil, pending, nil, tools, "slack", nil)

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

	prompt := BuildSystemPrompt("Test", nil, nil, nil, nil, nil, "", nil)
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

// === M1.5 commit 4: plan-task augmentation ===

func TestAppendPlanTaskInstructions_AppendsWhenPlanTask(t *testing.T) {
	RegisterTestingT(t)
	got := AppendPlanTaskInstructions("base prompt", "plan_task")
	Expect(got).To(ContainSubstring("base prompt"))
	Expect(got).To(ContainSubstring("PLAN TASK MODE"))
	Expect(got).To(ContainSubstring("set_output"))
	Expect(got).To(ContainSubstring("plan/block"))
}

func TestAppendPlanTaskInstructions_NoOpForUserChannels(t *testing.T) {
	// Augmentation must NOT fire for normal channel turns —
	// otherwise the AI would refuse to message the user during a
	// Telegram conversation.
	RegisterTestingT(t)
	for _, channel := range []string{"telegram", "slack", "telegram_voice", "email", "webhook", ""} {
		got := AppendPlanTaskInstructions("base prompt", channel)
		Expect(got).To(Equal("base prompt"), "channel %q should not get plan-task augmentation", channel)
	}
}

func TestAppendPlanTaskInstructions_Idempotent(t *testing.T) {
	// Calling twice doesn't duplicate the block — defends against a
	// future refactor that accidentally wires the augmentation in
	// two places (e.g. once in AssembleSystemPrompt + once in a
	// caller). Same idempotency posture the BlobToken append uses.
	RegisterTestingT(t)
	once := AppendPlanTaskInstructions("base", "plan_task")
	twice := AppendPlanTaskInstructions(once, "plan_task")
	Expect(twice).To(Equal(once))
}

func TestChannelTypePlanTask_ConstantMatchesTickValue(t *testing.T) {
	// Pin the constant value — the tick endpoint's trigger_data
	// populates channel_type with the literal "plan_task". Drift in
	// either side would silently disable the augmentation.
	RegisterTestingT(t)
	Expect(ChannelTypePlanTask).To(Equal("plan_task"))
}

func TestAssembleSystemPrompt_AppliesAugmentationForPlanTask(t *testing.T) {
	// Integration through the assembler — minimum-config path with
	// no user_id (degraded branch). The augmentation should still
	// fire because plan tasks have no user identity.
	RegisterTestingT(t)
	asm := NewSystemPromptAssembler(nil, nil, nil, 10)
	res := asm.AssembleSystemPrompt(SystemPromptRequest{
		AgentID:     "agent-1",
		Persona:     "You are a planning agent.",
		ChannelType: "plan_task",
	})
	Expect(res.Prompt).To(ContainSubstring("PLAN TASK MODE"))
	Expect(res.Prompt).To(ContainSubstring("You are a planning agent."))
}

func TestAssembleSystemPrompt_NoAugmentationForChannelTurns(t *testing.T) {
	RegisterTestingT(t)
	asm := NewSystemPromptAssembler(nil, nil, nil, 10)
	res := asm.AssembleSystemPrompt(SystemPromptRequest{
		AgentID:     "agent-1",
		Persona:     "You are a chat agent.",
		ChannelType: "telegram",
	})
	Expect(res.Prompt).NotTo(ContainSubstring("PLAN TASK MODE"))
}

// === M4 — draft-first plan authoring guidance ===

func TestAppendPlanAuthoringInstructions_AppendsForUserChannels(t *testing.T) {
	// Telegram, Slack, Manual — any non-plan-task channel — should
	// get the draft-first guidance so the AI knows plan/create
	// returns drafts and a separate plan/start call is needed.
	for _, ch := range []string{"telegram", "slack", "manual", ""} {
		out := AppendPlanAuthoringInstructions("Persona.", ch)
		if !strings.Contains(out, "PLAN AUTHORING") {
			t.Errorf("channel %q: missing PLAN AUTHORING block", ch)
		}
		if !strings.Contains(out, "plan/start") {
			t.Errorf("channel %q: PLAN AUTHORING block doesn't mention plan/start", ch)
		}
	}
}

func TestAppendPlanAuthoringInstructions_SkipsPlanTaskChannel(t *testing.T) {
	// Plan-task turns get PLAN TASK MODE which forbids plan/create
	// entirely. Adding draft-authoring guidance there would be
	// confusing — the AI shouldn't be authoring plans in plan-task
	// mode at all.
	out := AppendPlanAuthoringInstructions("Persona.", ChannelTypePlanTask)
	if strings.Contains(out, "PLAN AUTHORING") {
		t.Errorf("plan_task channel should NOT receive draft-authoring guidance, got: %s", out)
	}
}

func TestAppendPlanAuthoringInstructions_Idempotent(t *testing.T) {
	once := AppendPlanAuthoringInstructions("Persona.", "telegram")
	twice := AppendPlanAuthoringInstructions(once, "telegram")
	if once != twice {
		t.Errorf("idempotency broken — block appended twice")
	}
}

// === M6 — plan progress in system prompt ===

func TestAppendPlanStatusContext_EmptySummary_SkipsBlock(t *testing.T) {
	// Zero-plan agents must NOT receive an empty "PLAN STATUS — 0
	// plans" stub. The block is only injected when the summary
	// has at least one plan.
	out := AppendPlanStatusContext("Persona.", "telegram", apipersistence.PlanSummary{})
	if strings.Contains(out, "PLAN STATUS") {
		t.Errorf("empty summary should NOT add the PLAN STATUS block, got: %s", out)
	}
}

func TestAppendPlanStatusContext_NonEmptySummary_AppendsBlock(t *testing.T) {
	now := time.Now().Add(-5 * time.Minute)
	summary := apipersistence.PlanSummary{
		Draft:        1,
		Active:       1,
		Blocked:      1,
		LastActivity: &now,
	}
	out := AppendPlanStatusContext("Persona.", "telegram", summary)
	if !strings.Contains(out, "PLAN STATUS") {
		t.Errorf("missing PLAN STATUS block, got: %s", out)
	}
	if !strings.Contains(out, "3 plan(s)") {
		t.Errorf("missing plan count, got: %s", out)
	}
	if !strings.Contains(out, "1 draft") || !strings.Contains(out, "1 active") || !strings.Contains(out, "1 blocked") {
		t.Errorf("missing status breakdown, got: %s", out)
	}
	if !strings.Contains(out, "minute(s) ago") {
		t.Errorf("missing relative time, got: %s", out)
	}
}

func TestAppendPlanStatusContext_PlanTaskChannel_SkipsBlock(t *testing.T) {
	// Plan-task turns already get PLAN TASK MODE — M6's ambient
	// awareness would be confusing noise on top.
	summary := apipersistence.PlanSummary{Active: 1}
	out := AppendPlanStatusContext("Persona.", ChannelTypePlanTask, summary)
	if strings.Contains(out, "PLAN STATUS") {
		t.Errorf("plan_task channel should NOT receive the M6 block, got: %s", out)
	}
}

func TestAppendPlanStatusContext_Idempotent(t *testing.T) {
	summary := apipersistence.PlanSummary{Active: 1}
	once := AppendPlanStatusContext("Persona.", "telegram", summary)
	twice := AppendPlanStatusContext(once, "telegram", summary)
	if once != twice {
		t.Errorf("idempotency broken — M6 block appended twice")
	}
}

func TestAppendPlanStatusContext_OnlyShowsNonZeroLines(t *testing.T) {
	// One active, zero draft/blocked → only the "active" line
	// appears in the breakdown.
	summary := apipersistence.PlanSummary{Active: 2}
	out := AppendPlanStatusContext("Persona.", "telegram", summary)
	if strings.Contains(out, "draft") {
		t.Errorf("zero drafts should not produce a draft line, got: %s", out)
	}
	if strings.Contains(out, "blocked") {
		t.Errorf("zero blocked should not produce a blocked line, got: %s", out)
	}
	if !strings.Contains(out, "2 active") {
		t.Errorf("missing active line, got: %s", out)
	}
}

func TestFormatRelative_Buckets(t *testing.T) {
	// Boundary check on the relative-time renderer. Avoids time-
	// formatting libraries; just affirms our four buckets work.
	cases := []struct {
		dur     time.Duration
		want    string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "minute(s) ago"},
		{2 * time.Hour, "hour(s) ago"},
		{3 * 24 * time.Hour, "day(s) ago"},
	}
	for _, c := range cases {
		ts := time.Now().Add(-c.dur)
		got := formatRelative(ts)
		if !strings.Contains(got, c.want) {
			t.Errorf("dur=%s want substring=%q got=%q", c.dur, c.want, got)
		}
	}
}
