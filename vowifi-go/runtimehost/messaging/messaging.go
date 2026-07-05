package messaging

import (
	"context"
	"errors"
	"time"
)

var ErrDeliveryNotFound = errors.New("delivery not found")

type SendOptions struct {
	Encoding string
}

type SendOutcome struct {
	MessageID     string
	PartsTotal    int
	DeliveryState string
}

type USSDResult struct {
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Done      bool   `json:"done"`
	Raw       string `json:"raw,omitempty"`
}

type DeliveryPartMatch struct {
	MessageID string
	PartNo    int
	State     string
}

type DeliveryStatus struct {
	MessageID  string
	IMSI       string
	DeviceID   string
	Peer       string
	Content    string
	PartsTotal int
	Acks       int
	State      string
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Parts      []DeliveryPartStatus
}

type DeliveryPartStatus struct {
	PartNo      int
	CallID      string
	InReplyTo   string
	RPMR        int
	State       string
	SIPCode     int
	RPCause     int
	RPCauseText string
	ErrorText   string
	SentAt      time.Time
	ReportAt    *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DeliveryStore interface {
	CreateSMSDelivery(messageID, imsi, deviceID, peer, content string, partsTotal int, at time.Time) error
	UpsertSMSDeliveryPart(messageID string, partNo int, callID string, rpMR int, state string, sentAt time.Time) error
	MarkSMSDeliveryPartReport(inReplyTo, callID, deviceID string, rpMR int, state string, sipCode int, rpCause int, errText string, at time.Time) (DeliveryPartMatch, error)
	RecomputeSMSDelivery(messageID string, at time.Time) error
	UpdateSMSDeliveryState(messageID, state, lastError string, acks int, at time.Time) error
	GetSMSDeliveryStatus(messageID string) (*DeliveryStatus, error)
}

func RPCauseText(cause int) string {
	switch cause {
	case 0:
		return ""
	case 16:
		return "normal call clearing"
	case 17:
		return "user busy"
	case 21:
		return "short message transfer rejected"
	case 22:
		return "number changed"
	case 27:
		return "destination out of order"
	case 28:
		return "invalid number format"
	case 38:
		return "network out of order"
	case 41:
		return "temporary failure"
	case 42:
		return "switching equipment congestion"
	case 47:
		return "resource unavailable"
	case 95:
		return "invalid message"
	case 111:
		return "protocol error"
	default:
		return "rp cause"
	}
}

type suppressSendTGSuccessKey struct{}

func WithSuppressSendTGSuccess(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, suppressSendTGSuccessKey{}, true)
}

func SuppressSendTGSuccess(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(suppressSendTGSuccessKey{}).(bool)
	return v
}
