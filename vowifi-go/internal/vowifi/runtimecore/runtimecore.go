package runtimecore

import (
	"context"
	"errors"
	"net"
	"time"

	swusim "github.com/iniwex5/vowifi-go/engine/sim"
	"github.com/iniwex5/vowifi-go/runtimehost/carrier"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
)

var ErrUnsupportedPlatform = errors.New("vowifi swu runtime unsupported on this platform")

type SIMAdapter interface {
	GetIMSI() (string, error)
	CalculateAKA(rand, autn []byte) (swusim.AKAResult, error)
	Close() error
}

type ProxyConfig struct {
	Addr     string
	Username string
	Password string
	Enabled  bool
}

type StartInput struct {
	DeviceID        string
	Profile         identity.Profile
	Prepared        identity.PreparedSession
	SIM             SIMAdapter
	DataplaneMode   string
	Proxy           *ProxyConfig
	EnableDriver    bool
	TUNName         string
	WaitTimeout     time.Duration
	Logger          any
	ConnectOnReturn bool
}

type TunnelSnapshot struct {
	Established bool
	TUNName     string
	LastError   string
	IKEProfile  string
	IKEEncr     string
	IKEInteg    string
	IKEPRF      string
	IKEDH       string

	IPv4       net.IP
	IPv6       net.IP
	IPv6Prefix int

	DNSv4 []net.IP
	DNSv6 []net.IP

	PCSCFv4 []net.IP
	PCSCFv6 []net.IP

	OfferedIKEProfiles       []string
	EffectiveCipherPolicy    string
	NegotiationFallbackCount int
}

type Session interface {
	Snapshot() TunnelSnapshot
	Shutdown()
	WaitDoneContext(context.Context) error
	TriggerMOBIKE(oldIP, newIP string) error
}

func DefaultEPDGWaitTimeout() time.Duration {
	return 45 * time.Second
}

func CarrierFromPrepared(prepared identity.PreparedSession) carrier.Config {
	return prepared.EffectiveCarrier
}
