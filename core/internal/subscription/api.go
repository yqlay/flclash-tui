//go:build linux && !cgo && cli

package subscription

const (
	MaxBytes  = 32 << 20
	UserAgent = "mihomo"
)

const tuiSubscriptionMaxBytes = MaxBytes

type Payload = tuiSubscriptionPayload

func Normalize(data []byte) (Payload, error) {
	return normalizeTUISubscription(data)
}

func NewFileName(disposition string) string {
	return tuiNewSubscriptionFileName(disposition)
}

func ImportPath(homeDir string, payload Payload) (string, error) {
	return tuiSubscriptionImportPath(homeDir, payload)
}

func ImportedProfileName(sourceName string) string {
	return tuiImportedProfileName(sourceName)
}

func NextImportedProfilePath(homeDir, sourceName string) (string, error) {
	return nextTUIImportedProfilePath(homeDir, sourceName)
}

func (p tuiSubscriptionPayload) Summary() string {
	return p.summary()
}

func BuildProxyProfile(proxies []map[string]any, format string) (Payload, error) {
	return buildTUIProxyProfile(proxies, format)
}

func AnyMap(value any) (map[string]any, bool) {
	return tuiAnyMap(value)
}

func AnyString(value any) string {
	return tuiAnyString(value)
}
