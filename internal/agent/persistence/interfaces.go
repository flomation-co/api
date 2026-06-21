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

// PriorConversationsFetcher returns session_summary memories joined
// to their source conversations, so inbound dispatch can surface
// them in trigger data. Defined here (rather than imported wholesale)
// to keep the agent package's external surface narrow.
type PriorConversationsFetcher interface {
	GetRecentPriorConversations(agentID, agentUserID string, limit int) ([]apipersistence.PriorConversationSummary, error)
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

// BlobUploader writes inbound file bytes to the API's blob_object
// tier. Used by the inbound pipeline when a webhook arrived with file
// attachments base64-encoded inline (Launch can't talk to the blob
// endpoint directly — it doesn't carry the resolved scope). After
// upload the pipeline replaces the base64 with a flo:blob:... token
// before the agent_message is written, so the bytes never reach the
// LLM's context window.
//
// The BlobScope argument is org XOR owner — see
// internal/persistence/blob_object.go for the discriminated-union
// semantics. The inbound pipeline picks org when the agent is
// organisation-scoped and owner otherwise.
type BlobUploader interface {
	PutBlob(scope apipersistence.BlobScope, content []byte, mime, purpose string, execID *string) ([]byte, []byte, error)
}
