package identity

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/iniwex5/vowifi-go/runtimehost/carrier"
)

type Profile struct {
	IMSI string
	MCC  string
	MNC  string
	IMEI string
	SMSC string
}

type Identity struct {
	IMPI   string
	IMPU   []string
	Domain string
}

type PrepareStartInput struct {
	DeviceID            string
	Profile             Profile
	RuntimeEPDGOverride string
	Access              any
}

type IMSIdentity struct {
	RequestedSource  string
	ActualSource     string
	AKAAppPreference string
	Applied          bool
	IMPI             string
	IMPU             string
	Domain           string
}

type PreparedSession struct {
	Profile            Profile
	EffectiveCarrier   carrier.Config
	EPDGSource         string
	EPDGAddr           string
	IdentityIMEISource string
	IMSIdentity        IMSIdentity
}

const (
	IMSIdentitySourceISIM      = "isim"
	IMSIdentitySourceUSIM      = "usim"
	IMSIdentitySourceGenerated = "generated"
	AKAAppPreferenceAuto       = "auto"
	AKAAppPreferenceISIM       = "isim"
	AKAAppPreferenceISIMStrict = "isim_strict"
	AKAAppPreferenceUSIM       = "usim"
)

func NormalizeProfile(p Profile) Profile {
	p.IMSI = onlyDigits(p.IMSI)
	p.MCC = onlyDigits(p.MCC)
	p.MNC = onlyDigits(p.MNC)
	p.IMEI = onlyDigits(p.IMEI)
	p.SMSC = strings.TrimSpace(p.SMSC)
	if p.MCC == "" && len(p.IMSI) >= 3 {
		p.MCC = p.IMSI[:3]
	}
	if p.MNC == "" && len(p.IMSI) >= 6 {
		p.MNC = p.IMSI[3:6]
	}
	return p
}

func PrepareStart(input PrepareStartInput) (PreparedSession, error) {
	profile := NormalizeProfile(input.Profile)
	if strings.TrimSpace(profile.IMSI) == "" {
		return PreparedSession{}, errors.New("imsi unavailable")
	}
	if strings.TrimSpace(profile.MCC) == "" || strings.TrimSpace(profile.MNC) == "" {
		return PreparedSession{}, fmt.Errorf("plmn unavailable for imsi %s", profile.IMSI)
	}

	cfg := carrier.ResolveEffectiveCarrierConfig(carrier.EffectiveCarrierConfigInput{
		MCC: profile.MCC,
		MNC: profile.MNC,
	})
	prepared := PreparedSession{
		Profile:          profile,
		EffectiveCarrier: cfg,
		EPDGAddr:         strings.TrimSpace(cfg.EPDG.Host),
		EPDGSource:       "carrier",
		IMSIdentity: IMSIdentity{
			RequestedSource: strings.TrimSpace(cfg.IMS.IdentitySource),
		},
	}
	if prepared.IMSIdentity.RequestedSource == "" {
		prepared.IMSIdentity.RequestedSource = IMSIdentitySourceUSIM
	}
	if profile.IMEI != "" {
		prepared.IdentityIMEISource = "profile"
	}
	if override := strings.TrimSpace(input.RuntimeEPDGOverride); override != "" {
		prepared.EPDGAddr = override
		prepared.EPDGSource = "redirect"
	} else if prepared.EPDGAddr == "" {
		prepared.EPDGAddr = defaultEPDGHost(profile.MCC, profile.MNC)
		prepared.EPDGSource = "3gpp"
	}

	if isim, err := ReadISIMIdentity(input.Access); err == nil {
		if !completeISIMIdentity(isim) {
			return PreparedSession{}, fmt.Errorf("ISIM 身份不完整")
		}
		prepared.IMSIdentity.ActualSource = IMSIdentitySourceISIM
		prepared.IMSIdentity.AKAAppPreference = AKAAppPreferenceISIMStrict
		prepared.IMSIdentity.Applied = true
		prepared.IMSIdentity.IMPI = strings.TrimSpace(isim.IMPI)
		prepared.IMSIdentity.IMPU = strings.TrimSpace(isim.IMPU[0])
		prepared.IMSIdentity.Domain = strings.TrimSpace(isim.Domain)
		return prepared, nil
	}

	domain := strings.TrimSpace(cfg.IMS.Domain)
	if domain == "" {
		domain = defaultIMSDomain(profile.MCC, profile.MNC)
	}
	prepared.IMSIdentity.ActualSource = IMSIdentitySourceGenerated
	prepared.IMSIdentity.AKAAppPreference = AKAAppPreferenceUSIM
	prepared.IMSIdentity.Applied = true
	prepared.IMSIdentity.IMPI = profile.IMSI + "@" + domain
	prepared.IMSIdentity.IMPU = "sip:" + profile.IMSI + "@" + domain
	prepared.IMSIdentity.Domain = domain
	return prepared, nil
}

func ReadISIMIdentity(access any) (Identity, error) {
	if access == nil {
		return Identity{}, errors.New("isim access unavailable")
	}
	if unwrapper, ok := access.(interface{ RuntimeModem() any }); ok {
		if id, err := readISIMFromDirectReader(unwrapper.RuntimeModem()); err == nil {
			return id, nil
		}
	}
	if id, err := readISIMFromDirectReader(access); err == nil {
		return id, nil
	}
	return Identity{}, errors.New("isim identity unavailable")
}

func readISIMFromDirectReader(v any) (Identity, error) {
	if v == nil || isSelfDelegatingReader(v) {
		return Identity{}, errors.New("isim direct reader unavailable")
	}
	reader, ok := v.(interface {
		GetISIMIdentity() (Identity, error)
	})
	if !ok {
		return Identity{}, errors.New("isim direct reader unavailable")
	}
	id, err := reader.GetISIMIdentity()
	if err != nil {
		return Identity{}, err
	}
	id.IMPI = strings.TrimSpace(id.IMPI)
	id.Domain = strings.TrimSpace(id.Domain)
	for i := range id.IMPU {
		id.IMPU[i] = strings.TrimSpace(id.IMPU[i])
	}
	if id.IMPI == "" && id.Domain == "" && len(nonEmptyIMPU(id.IMPU)) == 0 {
		return Identity{}, errors.New("isim identity empty")
	}
	id.IMPU = nonEmptyIMPU(id.IMPU)
	return id, nil
}

func isSelfDelegatingReader(v any) bool {
	t := reflect.TypeOf(v)
	if t == nil {
		return false
	}
	name := t.String()
	return strings.Contains(name, ".modemAdapter") || strings.Contains(name, ".qmiModemAdapter")
}

func completeISIMIdentity(id Identity) bool {
	return strings.TrimSpace(id.IMPI) != "" && strings.TrimSpace(id.Domain) != "" && len(nonEmptyIMPU(id.IMPU)) > 0
}

func nonEmptyIMPU(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func defaultEPDGHost(mcc, mnc string) string {
	return fmt.Sprintf("epdg.epc.mnc%s.mcc%s.pub.3gppnetwork.org", padMNC3(mnc), onlyDigits(mcc))
}

func defaultIMSDomain(mcc, mnc string) string {
	return fmt.Sprintf("ims.mnc%s.mcc%s.3gppnetwork.org", padMNC3(mnc), onlyDigits(mcc))
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func padMNC3(mnc string) string {
	mnc = onlyDigits(mnc)
	if len(mnc) >= 3 {
		return mnc
	}
	return strings.Repeat("0", 3-len(mnc)) + mnc
}
