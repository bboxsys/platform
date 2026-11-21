package client

// NATS subjects published by the admin service for tenant lifecycle events.
// Subscribers (e.g., media-server) react to these to keep tenant-scoped
// resources in sync with the admin account store.
const (
	SubjectTenantCreated = "tenant.notify.created"
	SubjectTenantUpdated = "tenant.notify.updated"
	SubjectTenantDeleted = "tenant.notify.deleted"
)

// TenantCreatedNotification is published when a new account is created.
type TenantCreatedNotification struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	Timestamp   int64  `json:"timestamp"`
}

// TenantUpdatedNotification is published when an account's mutable
// fields (name, description, etc.) change. PreviousName is set when a
// rename occurred so subscribers can map old → new resources.
type TenantUpdatedNotification struct {
	AccountID    string `json:"accountId"`
	AccountName  string `json:"accountName"`
	PreviousName string `json:"previousName,omitempty"`
	Timestamp    int64  `json:"timestamp"`
}

// TenantDeletedNotification is published when an account is deleted.
// AccountName carries the last-known name since the account record is
// gone by the time subscribers receive this.
type TenantDeletedNotification struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	Timestamp   int64  `json:"timestamp"`
}
