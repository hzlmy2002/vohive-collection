package carrier

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type LoadResult struct {
	Path    string
	Missing bool
	Count   int
}

type EffectiveCarrierConfigInput struct {
	MCC string
	MNC string
}

type Config struct {
	MCC      string
	MNC      string
	PresetID string
	EPDG     EPDGConfig
	IMS      IMSConfig
	SMS      SMSConfig
	E911     E911Config
	IKE      IKEConfig
}

type EPDGConfig struct {
	Host      string
	Port      int
	IPStack   string
	APN       string
	DNSServer string
}

type IMSConfig struct {
	Domain          string
	Realm           string
	Registrar       string
	PCSCF           string
	Transport       string
	LocalPort       int
	UserAgent       string
	IdentitySource  string
	RegisterTimeout int
}

type SMSConfig struct {
	ReceiverTransport string
}

type E911Config struct {
	Enabled            bool
	Provider           string
	EntitlementURL     string
	WebsheetHostPolicy string
}

type IKEConfig struct {
	IKEProposals   []string
	ESPProposals   []string
	IncludeEPDGIDr bool
}

var overrideCount int

func LoadCarrierOverrides(path string) (LoadResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return LoadResult{Missing: true}, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadResult{Path: path, Missing: true}, nil
		}
		return LoadResult{Path: path}, err
	}
	overrideCount = 0
	return LoadResult{Path: path, Count: overrideCount}, nil
}

func ClearCarrierOverrides() {
	overrideCount = 0
}

func ResolveEffectiveCarrierConfig(input EffectiveCarrierConfigInput) Config {
	mcc := normalizeDigits(input.MCC)
	mnc := normalizeDigits(input.MNC)
	cfg := fallbackConfig(mcc, mnc)

	switch plmnKey(mcc, mnc) {
	case "310/280":
		cfg.PresetID = "att_310280"
		cfg.EPDG.Host = "epdg.epc.att.net"
		cfg.IMS.IdentitySource = "isim"
		cfg.IKE.IKEProposals = []string{"aes128-sha256-prfsha1-modp2048"}
		cfg.IKE.ESPProposals = []string{"aes128-sha256"}
		cfg.E911 = E911Config{
			Enabled:            true,
			Provider:           "att_entitlement",
			EntitlementURL:     "https://sentitlement2.mobile.att.net/",
			WebsheetHostPolicy: "public_https",
		}
	case "310/410":
		cfg.PresetID = "att_310410"
		cfg.EPDG.Host = "epdg.epc.att.net"
		cfg.IMS.IdentitySource = "isim"
		cfg.IKE.IKEProposals = []string{"aes128-sha256-prfsha1-modp2048"}
		cfg.IKE.ESPProposals = []string{"aes128-sha256"}
		cfg.E911 = E911Config{
			Enabled:            true,
			Provider:           "att_entitlement",
			EntitlementURL:     "https://sentitlement2.mobile.att.net/",
			WebsheetHostPolicy: "public_https",
		}
	case "310/240":
		cfg.PresetID = "tmobile_310240"
		cfg.IKE.ESPProposals = []string{"aes128-sha256", "aes128-sha1"}
		cfg.E911 = E911Config{
			Enabled:            true,
			Provider:           "T-Mobile_entitlement",
			EntitlementURL:     "https://eas3.msg.t-mobile.com/",
			WebsheetHostPolicy: "public_https",
		}
	case "310/260":
		cfg.PresetID = "tmobile_310260"
		cfg.IKE.ESPProposals = []string{"aes128-sha256", "aes128-sha1"}
		cfg.E911 = E911Config{
			Enabled:            true,
			Provider:           "T-Mobile_entitlement",
			EntitlementURL:     "https://eas3.msg.t-mobile.com/",
			WebsheetHostPolicy: "public_https",
		}
	case "234/33":
		cfg.PresetID = "cteuk_23433"
	case "234/10":
		cfg.PresetID = "giffgaff_23410"
		cfg.IKE.IKEProposals = []string{"aes256-sha512-prfsha512-modp2048"}
		cfg.IKE.ESPProposals = []string{"aes256-sha512"}
	case "204/4":
		cfg.PresetID = "vodafone_nl_20404"
		cfg.EPDG.APN = "ims"
	case "262/3":
		cfg.PresetID = "o2_de_26203"
		cfg.EPDG.Host = "epdg.epc.mnc003.mcc262.pub.3gppnetwork.org"
	case "262/7":
		cfg.PresetID = "o2_de_26207_alias"
		cfg.EPDG.Host = "epdg.epc.mnc007.mcc262.pub.3gppnetwork.org"
	}

	return cfg
}

func IsVoWiFiBlockedMCC(mcc string) bool {
	switch normalizeDigits(mcc) {
	case "460", "461":
		return true
	default:
		return false
	}
}

type blockedMCCError struct {
	mcc string
}

func (e blockedMCCError) Error() string {
	if e.mcc == "" {
		return "vowifi blocked by carrier policy"
	}
	return fmt.Sprintf("vowifi blocked by carrier policy for MCC %s", e.mcc)
}

func (e blockedMCCError) Is(target error) bool {
	_, ok := target.(blockedMCCError)
	return ok
}

var errBlockedMCC = blockedMCCError{}

func NewVoWiFiBlockedMCCError(mcc string) error {
	return blockedMCCError{mcc: normalizeDigits(mcc)}
}

func IsVoWiFiPolicyBlockedError(err error) bool {
	return errors.Is(err, errBlockedMCC)
}

func fallbackConfig(mcc, mnc string) Config {
	mnc3 := padMNC3(mnc)
	domain := ""
	if mcc != "" && mnc3 != "" {
		domain = fmt.Sprintf("ims.mnc%s.mcc%s.3gppnetwork.org", mnc3, mcc)
	}
	epdg := ""
	if mcc != "" && mnc3 != "" {
		epdg = fmt.Sprintf("epdg.epc.mnc%s.mcc%s.pub.3gppnetwork.org", mnc3, mcc)
	}
	return Config{
		MCC:      mcc,
		MNC:      mnc,
		PresetID: "3gpp_fallback",
		EPDG: EPDGConfig{
			Host:    epdg,
			Port:    500,
			IPStack: "dual",
			APN:     "ims",
		},
		IMS: IMSConfig{
			Domain:    domain,
			Realm:     domain,
			Registrar: domain,
			Transport: "udp",
			LocalPort: 5060,
			UserAgent: "VoHive/1.0",
		},
		SMS: SMSConfig{ReceiverTransport: "ims"},
		IKE: IKEConfig{
			IKEProposals: []string{"aes256-sha256-prfsha1-modp2048"},
			ESPProposals: []string{"aes256-sha256", "aes128-sha256"},
		},
	}
}

func plmnKey(mcc, mnc string) string {
	return mcc + "/" + strings.TrimLeft(mnc, "0")
}

func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func padMNC3(mnc string) string {
	mnc = normalizeDigits(mnc)
	if len(mnc) >= 3 {
		return mnc
	}
	return strings.Repeat("0", 3-len(mnc)) + mnc
}
