//go:build linux

package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	simusw "github.com/iniwex5/swu-go/pkg/sim"
	swu "github.com/iniwex5/swu-go/pkg/swu"
	swusim "github.com/iniwex5/vowifi-go/engine/sim"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
	"go.uber.org/zap"
)

type swuSession struct {
	session  *swu.Session
	epdgAddr string
}

func BuildSWUConfig(input StartInput) (*swu.Config, error) {
	deviceID := strings.TrimSpace(input.DeviceID)
	if deviceID == "" {
		return nil, errors.New("device_id is empty")
	}
	if input.SIM == nil {
		return nil, errors.New("sim adapter unavailable")
	}

	profile := identity.NormalizeProfile(input.Profile)
	if strings.TrimSpace(profile.IMSI) == "" {
		profile = identity.NormalizeProfile(input.Prepared.Profile)
	}
	if strings.TrimSpace(profile.IMSI) == "" {
		return nil, errors.New("imsi unavailable")
	}

	cfg := input.Prepared.EffectiveCarrier
	epdgAddr := strings.TrimSpace(input.Prepared.EPDGAddr)
	if epdgAddr == "" {
		epdgAddr = strings.TrimSpace(cfg.EPDG.Host)
	}
	if epdgAddr == "" {
		return nil, errors.New("epdg address unavailable")
	}

	epdgPort := uint16(cfg.EPDG.Port)
	if epdgPort == 0 {
		epdgPort = 500
	}
	apn := strings.TrimSpace(cfg.EPDG.APN)
	if apn == "" {
		apn = "ims"
	}

	out := &swu.Config{
		DeviceID:            deviceID,
		EpDGAddr:            epdgAddr,
		EpDGPort:            epdgPort,
		APN:                 apn,
		DNSServer:           strings.TrimSpace(cfg.EPDG.DNSServer),
		SIM:                 swuSIMAdapter{imsi: profile.IMSI, sim: input.SIM},
		EnableDriver:        input.EnableDriver,
		DataplaneMode:       mapDataplaneMode(input.DataplaneMode),
		MCC:                 profile.MCC,
		MNC:                 profile.MNC,
		TUNName:             strings.TrimSpace(input.TUNName),
		IKEProposals:        append([]string(nil), cfg.IKE.IKEProposals...),
		ESPProposals:        append([]string(nil), cfg.IKE.ESPProposals...),
		NATKeepaliveSeconds: 20,
		DPDIntervalSeconds:  30,
		AlgorithmPolicy:     swu.AlgorithmPolicyBalanced,
	}
	if out.TUNName == "" {
		out.TUNName = defaultTUNName(deviceID)
	}
	if proxy := input.Proxy; proxy != nil && proxy.Enabled && strings.TrimSpace(proxy.Addr) != "" {
		out.Socks5Addr = strings.TrimSpace(proxy.Addr)
		out.Socks5Username = strings.TrimSpace(proxy.Username)
		out.Socks5Password = proxy.Password
	}
	if strings.TrimSpace(profile.IMEI) != "" {
		out.DeviceIdentityIMEI = strings.TrimSpace(profile.IMEI)
	}
	if mode := strings.TrimSpace(input.Prepared.IMSIdentity.AKAAppPreference); mode != "" {
		out.AKAIdentityMode = mode
	}
	return out, nil
}

func StartAndWaitEPDG(ctx context.Context, input StartInput) (Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := BuildSWUConfig(input)
	if err != nil {
		return nil, err
	}

	zl, _ := input.Logger.(*zap.Logger)
	if zl == nil {
		zl = zap.NewNop()
	}
	session := swu.NewSession(cfg, zl)
	wrapped := &swuSession{session: session, epdgAddr: cfg.EpDGAddr}

	connectCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- session.Connect(connectCtx)
	}()

	timeout := input.WaitTimeout
	if timeout <= 0 {
		timeout = DefaultEPDGWaitTimeout()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cancel()
			_ = session.WaitDoneContext(context.Background())
			return nil, ctx.Err()
		case err := <-errCh:
			cancel()
			if err == nil {
				err = errors.New("swu session ended before tunnel establishment")
			}
			return nil, err
		case <-timer.C:
			cancel()
			_ = session.WaitDoneContext(context.Background())
			return nil, fmt.Errorf("epdg tunnel establishment timed out after %s", timeout)
		case <-ticker.C:
			snap := session.Snapshot()
			if snap.Established {
				return wrapped, nil
			}
			if snap.LastError != "" {
				cancel()
				_ = session.WaitDoneContext(context.Background())
				return nil, errors.New(snap.LastError)
			}
		}
	}
}

func (s *swuSession) Snapshot() TunnelSnapshot {
	if s == nil || s.session == nil {
		return TunnelSnapshot{}
	}
	return convertSnapshot(s.session.Snapshot())
}

func (s *swuSession) Shutdown() {
	if s != nil && s.session != nil {
		s.session.Shutdown()
	}
}

func (s *swuSession) WaitDoneContext(ctx context.Context) error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.WaitDoneContext(ctx)
}

func (s *swuSession) TriggerMOBIKE(oldIP, newIP string) error {
	if s == nil || s.session == nil {
		return errors.New("swu session unavailable")
	}
	newLocal := strings.TrimSpace(newIP)
	if newLocal == "" {
		return errors.New("new local address is empty")
	}
	return s.session.UpdateAddresses(newLocal, s.epdgAddr)
}

type swuSIMAdapter struct {
	imsi string
	sim  SIMAdapter
}

func (a swuSIMAdapter) GetIMSI() (string, error) {
	if strings.TrimSpace(a.imsi) != "" {
		return strings.TrimSpace(a.imsi), nil
	}
	if a.sim == nil {
		return "", errors.New("sim adapter unavailable")
	}
	return a.sim.GetIMSI()
}

func (a swuSIMAdapter) CalculateAKA(rand, autn []byte) (res, ck, ik, auts []byte, err error) {
	if a.sim == nil {
		return nil, nil, nil, nil, errors.New("sim adapter unavailable")
	}
	r, err := a.sim.CalculateAKA(rand, autn)
	if err != nil {
		if errors.Is(err, swusim.ErrSyncFailure) {
			return nil, nil, nil, append([]byte(nil), r.AUTS...), simusw.ErrSyncFailure
		}
		if len(r.AUTS) > 0 {
			return nil, nil, nil, append([]byte(nil), r.AUTS...), simusw.ErrSyncFailure
		}
		return nil, nil, nil, nil, err
	}
	return append([]byte(nil), r.RES...), append([]byte(nil), r.CK...), append([]byte(nil), r.IK...), append([]byte(nil), r.AUTS...), nil
}

func (a swuSIMAdapter) Close() error {
	if a.sim == nil {
		return nil
	}
	return a.sim.Close()
}

func convertSnapshot(in swu.SessionSnapshot) TunnelSnapshot {
	return TunnelSnapshot{
		Established:              in.Established,
		TUNName:                  in.TUNName,
		LastError:                in.LastError,
		IKEProfile:               in.IKEProfile,
		IKEEncr:                  in.IKEEncr,
		IKEInteg:                 in.IKEInteg,
		IKEPRF:                   in.IKEPRF,
		IKEDH:                    in.IKEDH,
		IPv4:                     append([]byte(nil), in.IPv4...),
		IPv6:                     append([]byte(nil), in.IPv6...),
		IPv6Prefix:               in.IPv6Prefix,
		DNSv4:                    cloneIPs(in.DNSv4),
		DNSv6:                    cloneIPs(in.DNSv6),
		PCSCFv4:                  cloneIPs(in.PCSCFv4),
		PCSCFv6:                  cloneIPs(in.PCSCFv6),
		OfferedIKEProfiles:       append([]string(nil), in.OfferedIKEProfiles...),
		EffectiveCipherPolicy:    in.EffectiveCipherPolicy,
		NegotiationFallbackCount: in.NegotiationFallbackCount,
	}
}

func cloneIPs(in []net.IP) []net.IP {
	out := make([]net.IP, 0, len(in))
	for _, ip := range in {
		out = append(out, append(net.IP(nil), ip...))
	}
	return out
}

func mapDataplaneMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "xfrmi", "xfrm":
		return "xfrmi"
	default:
		return "tun"
	}
}

func defaultTUNName(deviceID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(deviceID)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	suffix := b.String()
	if suffix == "" {
		suffix = "0"
	}
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return "vw" + suffix
}
