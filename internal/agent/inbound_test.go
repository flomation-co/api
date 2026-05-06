package agent

import (
	"testing"

	api "flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func TestDeriveExternalID_Slack(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "slack",
		Sender:      "Andy",
		Metadata: map[string]interface{}{
			"user_id":   "U12345",
			"user_name": "andy.esser",
		},
	}

	id, name := DeriveExternalID(msg)
	Expect(id).To(Equal("U12345"))
	Expect(name).To(Equal("andy.esser"))
}

func TestDeriveExternalID_Telegram(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "telegram",
		Sender:      "Andy",
		Metadata: map[string]interface{}{
			"sender_id":       "67890",
			"sender_username": "andyesser",
		},
	}

	id, name := DeriveExternalID(msg)
	Expect(id).To(Equal("67890"))
	Expect(name).To(Equal("@andyesser"))
}

func TestDeriveExternalID_Email(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "email",
		Sender:      "andy",
		Metadata:    map[string]interface{}{"from": "Andy Esser <andy@flomation.co>"},
	}

	id, name := DeriveExternalID(msg)
	Expect(id).To(Equal("andy@flomation.co"))
	Expect(name).To(Equal("Andy Esser <andy@flomation.co>"))
}

func TestDeriveExternalID_BareEmail(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "email",
		Sender:      "andy",
		Metadata:    map[string]interface{}{"from": "andy@flomation.co"},
	}

	id, _ := DeriveExternalID(msg)
	Expect(id).To(Equal("andy@flomation.co"))
}

func TestDeriveExternalID_Fallback(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "webhook",
		Sender:      "system",
		Metadata:    map[string]interface{}{},
	}

	id, name := DeriveExternalID(msg)
	Expect(id).To(Equal("system"))
	Expect(name).To(Equal("system"))
}

func TestDeriveExternalID_NilMetadata(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "slack",
		Sender:      "fallback",
	}

	id, name := DeriveExternalID(msg)
	Expect(id).To(Equal("fallback"))
	Expect(name).To(Equal("fallback"))
}

func TestDeriveChannelScope_Slack(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "slack",
		Metadata: map[string]interface{}{
			"channel_id": "C123",
			"thread_ts":  "1234567890.123456",
		},
	}

	channelID, threadID := DeriveChannelScope(msg)
	Expect(channelID).To(Equal("C123"))
	Expect(threadID).NotTo(BeNil())
	Expect(*threadID).To(Equal("1234567890.123456"))
}

func TestDeriveChannelScope_SlackNoThread(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "slack",
		Metadata:    map[string]interface{}{"channel_id": "C123"},
	}

	channelID, threadID := DeriveChannelScope(msg)
	Expect(channelID).To(Equal("C123"))
	Expect(threadID).To(BeNil())
}

func TestDeriveChannelScope_Telegram(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "telegram",
		Metadata:    map[string]interface{}{"chat_id": "12345"},
	}

	channelID, threadID := DeriveChannelScope(msg)
	Expect(channelID).To(Equal("12345"))
	Expect(threadID).To(BeNil())
}

func TestDeriveChannelScope_Email(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{
		ChannelType: "email",
		Metadata: map[string]interface{}{
			"account":   "andy@flomation.co",
			"thread_id": "thread-abc",
		},
	}

	channelID, threadID := DeriveChannelScope(msg)
	Expect(channelID).To(Equal("andy@flomation.co"))
	Expect(threadID).NotTo(BeNil())
	Expect(*threadID).To(Equal("thread-abc"))
}

func TestDeriveChannelScope_NilMetadata(t *testing.T) {
	RegisterTestingT(t)

	msg := InboundMessage{ChannelType: "slack"}
	channelID, threadID := DeriveChannelScope(msg)
	Expect(channelID).To(Equal(""))
	Expect(threadID).To(BeNil())
}

func TestExtractBareEmail(t *testing.T) {
	RegisterTestingT(t)

	Expect(extractBareEmail("Andy Esser <andy@flomation.co>")).To(Equal("andy@flomation.co"))
	Expect(extractBareEmail("andy@flomation.co")).To(Equal("andy@flomation.co"))
	Expect(extractBareEmail("<andy@flomation.co>")).To(Equal("andy@flomation.co"))
	Expect(extractBareEmail("")).To(Equal(""))
}

func TestExtractEmailBody(t *testing.T) {
	RegisterTestingT(t)

	// Full email with headers and quoted reply
	full := "From: Andy <andy@test.com>\nSubject: Re: Hello\n\nYes, that sounds good.\n\nOn Mon wrote:\n> original message"
	Expect(extractEmailBody(full)).To(Equal("Yes, that sounds good."))

	// Simple body without headers
	simple := "Just a simple reply"
	Expect(extractEmailBody(simple)).To(Equal("Just a simple reply"))
}

func TestAffirmatives(t *testing.T) {
	RegisterTestingT(t)

	Expect(affirmatives["yes"]).To(BeTrue())
	Expect(affirmatives["sure"]).To(BeTrue())
	Expect(affirmatives["go ahead"]).To(BeTrue())
	Expect(affirmatives["nope"]).To(BeFalse())
	Expect(affirmatives["hello"]).To(BeFalse())
}

func TestDecliners(t *testing.T) {
	RegisterTestingT(t)

	Expect(decliners["no"]).To(BeTrue())
	Expect(decliners["cancel"]).To(BeTrue())
	Expect(decliners["yes"]).To(BeFalse())
}

func TestNormaliseMessages(t *testing.T) {
	RegisterTestingT(t)

	msgs := []*api.AgentMessage{
		{Direction: "inbound", Content: "Hello"},
		{Direction: "outbound", Content: "Hi there"},
		{Direction: "tool_use", Content: "[Tool Call] email_send"},
		{Direction: "tool_result", Content: "[Tool Result] sent"},
	}

	result := normaliseMessages(msgs)
	Expect(result).To(HaveLen(2))
	Expect(result[0]["role"]).To(Equal("user"))
	Expect(result[1]["role"]).To(Equal("assistant"))
}
