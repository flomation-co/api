// Package persistence defines the interfaces the agent package needs
// from the persistence layer. Using interfaces keeps the agent package
// testable without importing the full persistence service.
package persistence

import (
	api "flomation.app/automate/api"
	apipersistence "flomation.app/automate/api/internal/persistence"
)

// IdentityResolver resolves inbound message senders to agent users.
type IdentityResolver interface {
	ResolveOrCreateAgentIdentity(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string) (*api.AgentIdentity, *api.AgentUser, error)
	ResolveOrCreateAgentIdentityWithSecondary(agentID string, organisationID *string, channelType, externalID string, scope *string, displayName *string, secondaryExternalID *string) (*api.AgentIdentity, *api.AgentUser, error)
}

// ConversationResolver opens or continues conversations.
type ConversationResolver interface {
	ResolveOrCreateAgentConversation(agentID string, agentUserID *string, channelType, channelID string, threadID *string, idleTimeout int) (*apipersistence.ConversationResolution, error)
}

// MessageStore stores and retrieves agent messages.
type MessageStore interface {
	CreateAgentMessageInConversation(msg api.AgentMessage) (*string, error)
}

// HistoryFetcher retrieves conversation message history.
type HistoryFetcher interface {
	GetAgentConversationMessages(conversationID string, limit int) ([]*api.AgentMessage, error)
}

// ExtractionDispatcher dispatches extraction pipeline runs.
// This interface wraps the extraction logic so the agent package
// doesn't need to know about HTTP handlers or flow execution details.
type ExtractionDispatcher interface {
	DispatchExtraction(agentID, content, role string, msgID, agentUserID, conversationID *string)
}

// PendingActionChecker handles pending action confirmation detection.
type PendingActionChecker interface {
	GetOpenPendingActionsForUser(agentUserID string) ([]*api.AgentPendingAction, error)
	UpdatePendingActionStatus(id, status string) error
	RequestCrossChannelVerification(agentID, pendingActionID, agentUserID string)
	TriggerIdentityMerge(agentID, pendingActionID string)
}
